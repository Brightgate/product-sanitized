//
// COPYRIGHT 2020 Brightgate Inc.  All rights reserved.
//
// This copyright notice is Copyright Management Information under 17 USC 1202
// and is included to protect this work and deter copyright infringement.
// Removal or alteration of this Copyright Management Information without the
// express written permission of Brightgate Inc is prohibited, and any
// such unauthorized removal or alteration will be a violation of federal law.
//

package main

import (
	"database/sql"
	"fmt"
	"io"
	"io/ioutil"
	"regexp"
	"time"

	"bg/base_msg"

	"github.com/golang/protobuf/proto"
	"github.com/jmoiron/sqlx"
	"github.com/klauspost/oui"
	"github.com/pkg/errors"
	"github.com/satori/uuid"
	"github.com/yourbasic/bloom"
)

var (
	siteBF     *bloom.Filter
	filepathRe *regexp.Regexp
)

func getContentStatusFromReader(rdr io.Reader) string {
	entityPresent := "-"
	dhcpPresent := "-"
	dnsRecordsPresent := "-"
	networkScanPresent := "-"

	buf, err := ioutil.ReadAll(rdr)
	if err != nil {
		slog.Warnf("could not read: %v\n", err)
		return "????"
	}

	di := &base_msg.DeviceInfo{}
	err = proto.Unmarshal(buf, di)
	if err != nil {
		slog.Warnf("could not unmarshal content: %v\n", err)
		return "????"
	}

	if di.Entity != nil {
		entityPresent = "E"
	}

	if len(di.Options) > 0 {
		dhcpPresent = "D"
	}

	if len(di.Request) > 0 {
		dnsRecordsPresent = "N"
	}

	if len(di.Scan) > 0 {
		networkScanPresent = "S"
	}

	return fmt.Sprintf("%s%s%s%s", entityPresent, dhcpPresent,
		dnsRecordsPresent, networkScanPresent)
}

// insertNewSiteByUUID adds a new site to the 'site' table if it isn't
// present.  To keep things fast, we use a bloom filter to remember
// which sites we've already seen/inserted.
func insertNewSiteByUUID(db *sqlx.DB, UUID uuid.UUID) int {
	var ruuid string

	if siteBF.TestByte(UUID.Bytes()) {
		// Already present.
		return 0
	}

	row := db.QueryRow("SELECT site_uuid FROM site WHERE site_uuid = $1;", UUID.String())
	err := row.Scan(&ruuid)
	if err == nil {
		// Already present.
		return 0
	} else if err == sql.ErrNoRows {
		_, err := db.Exec("INSERT INTO site (site_uuid, site_name) VALUES ($1, $2);", UUID.String(), unknownSite)
		if err != nil {
			slog.Fatalf("insert site failed: %v\n", err)
		}

		siteBF.AddByte(UUID.Bytes())
		return 1
	}

	slog.Errorf("site scan err %v\n", err)
	// No addition due to error.
	return 0
}

// getSiteIngestTimes builds a map of sites, with previous ingest
// timestamp.  When we do our subsequent ingest, we consider records
// newer than this timestamp.
func getSiteIngestTimes(db *sqlx.DB) (map[uuid.UUID]time.Time, error) {
	ingestTimes := make(map[uuid.UUID]time.Time)
	rows, err := db.Queryx(`
		SELECT site_uuid, MAX(ingest_date)
		FROM ingest
		GROUP BY site_uuid;`)
	if err != nil && err != sql.ErrNoRows {
		return nil, errors.Wrap(err, "select ingest scan failed")
	}
	defer rows.Close()
	for rows.Next() {
		var uuStr string
		var tStr string
		err := rows.Scan(&uuStr, &tStr)
		if err != nil {
			return nil, errors.Wrap(err, "ingest row failed")
		}
		uu, err := uuid.FromString(uuStr)
		if err != nil {
			return nil, errors.Wrapf(err, "parse uuid %q failed", uuStr)
		}
		t, err := time.Parse("2006-01-02 15:04:05.999-07:00", tStr)
		if err != nil {
			return nil, errors.Wrapf(err, "parse time %s failed", tStr)
		}
		ingestTimes[uu] = t
	}
	return ingestTimes, nil
}

// insertSiteIngest adds a record to the site ingest table representing the
// results of an ingest run for a site.
func insertSiteIngest(db *sqlx.DB, ingest *RecordedIngest) error {
	if ingest.SiteUUID == "" || ingest.IngestDate.IsZero() {
		slog.Fatalf("malformed RecordedIngest %v", ingest)
	}
	_, err := db.NamedExec(`
		INSERT OR REPLACE INTO ingest (ingest_date, site_uuid, new_inventories)
		VALUES (:ingest_date, :site_uuid, :new_inventories)`,
		ingest)
	return err
}

func readerFromTraining(B *backdrop, rt RecordedTraining) (io.Reader, error) {
	return B.ingester.DeviceInfoOpen(B, rt.SiteUUID, rt.DeviceMAC, rt.UnixTimestamp)
}

func readerFromRecord(B *backdrop, rdi RecordedInventory) (io.Reader, error) {
	return B.ingester.DeviceInfoOpen(B, rdi.SiteUUID, rdi.DeviceMAC, rdi.UnixTimestamp)
}

func inventoryFromTraining(db *sqlx.DB, rt RecordedTraining) (*RecordedInventory, error) {
	ri := RecordedInventory{}
	err := db.Get(&ri, `
		SELECT * FROM inventory
		WHERE site_uuid=$1 AND device_mac=$2 AND unix_timestamp = $3;`,
		rt.SiteUUID, rt.DeviceMAC, rt.UnixTimestamp)
	if err != nil {
		return nil, errors.Wrap(err, "inventoryFromTraining failed")
	}
	return &ri, nil
}

// addInfoFromReader adds information from the protobuf in rdr into the
// inventory record.  This includes the Sentence and DHCP information.
func (r *RecordedInventory) addInfoFromReader(ouiDB oui.OuiDB, rdr io.Reader) error {
	buf, err := ioutil.ReadAll(rdr)
	if err != nil {
		return errors.Wrap(err, "couldn't ReadAll")
	}

	di := &base_msg.DeviceInfo{}
	err = proto.Unmarshal(buf, di)
	if err != nil {
		return errors.Wrap(err, "couldn't unmarshal inventory record")
	}

	// Extract DHCP vendor raw string.
	_, r.DHCPVendor = extractDeviceInfoDHCP(di)

	// Add sentence of extracted features
	sentenceVersion, sentence := genBayesSentenceFromDeviceInfo(ouiDB, di)
	r.BayesSentenceVersion = sentenceVersion
	r.BayesSentence = sentence.toString()
	return nil
}

func recordInventory(db *sqlx.DB, stats *RecordedIngest, inventory *RecordedInventory) error {
	_, err := db.NamedExec(`INSERT OR REPLACE INTO inventory
		(storage, inventory_date, unix_timestamp,
		 site_uuid, device_mac, dhcp_vendor,
		 bayes_sentence_version, bayes_sentence)
		VALUES (:storage, :inventory_date, :unix_timestamp,
		 :site_uuid, :device_mac, :dhcp_vendor,
		 :bayes_sentence_version, :bayes_sentence)`, inventory)
	if err != nil {
		return errors.Wrapf(err, "insert inventory %v failed", inventory)
	}
	stats.Lock()
	defer stats.Unlock()
	stats.NewInventories++

	// We want to update the ingest cache value to the maximum time we see.
	if inventory.InventoryDate.After(stats.IngestDate) {
		stats.IngestDate = inventory.InventoryDate
	}
	return nil
}

// countOtherSentenceVersions counts how many of the site's records do not
// match the supplied sentence version.  Counting is not strictly necessary
// (we could use EXISTS for performance) but it is more ergonomic for
// anyone trying to debug things.
func countOtherSentenceVersions(db *sqlx.DB, siteUUID uuid.UUID, version string) (int64, error) {
	row := db.QueryRow(`
		SELECT COUNT (1) FROM inventory
		WHERE site_uuid = $1 AND bayes_sentence_version != $2;`,
		siteUUID, version)

	var old int64
	err := row.Scan(&old)
	if err != nil {
		return 0, errors.Wrap(err, "checkSentenceVersion")
	}
	return old, nil
}

// removeOtherSentenceVersions removes any rows from the site's inventory
// which do not match the supplied version.
func removeOtherSentenceVersions(db *sqlx.DB, siteUUID uuid.UUID, version string) error {
	_, err := db.Exec(`
		DELETE FROM inventory
		WHERE site_uuid = $1 AND bayes_sentence_version != $2
		);`, siteUUID, version)
	return err
}

func init() {
	siteBF = bloom.New(10000, 500)

	filepathRe = regexp.MustCompile(filepathPattern)
}
