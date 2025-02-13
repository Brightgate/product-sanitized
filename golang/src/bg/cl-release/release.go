/*
 * Copyright 2020 Brightgate Inc.
 *
 * This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at https://mozilla.org/MPL/2.0/.
 */


package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"bg/cl_common/clcfg"
	"bg/cl_common/pgutils"
	"bg/cl_common/registry"
	"bg/cl_common/release"
	"bg/cloud_models/appliancedb"
	"bg/common/cfgapi"

	"github.com/pkg/errors"
	"github.com/satori/uuid"
	"github.com/spf13/cobra"
	"github.com/tatsushid/go-prettytable"
	"github.com/tomazk/envcfg"
)

// Cfg defines the environment variables used to configure the app
type Cfg struct {
	PostgresConnection       string `envcfg:"B10E_CLRELEASE_POSTGRES_CONNECTION"`
	BackupPostgresConnection string `envcfg:"REG_DBURI"`

	Platform string `envcfg:"B10E_CLRELEASE_PLATFORM"`
	Prefix   string `envcfg:"B10E_CLRELEASE_PREFIX"`

	ConfigdConnection string `envcfg:"B10E_CLRELEASE_CLCONFIGD_CONNECTION"`
	// Whether to Disable TLS for outbound connections to cl.configd
	ConfigdDisableTLS bool `envcfg:"B10E_CLRELEASE_CLCONFIGD_DISABLE_TLS"`
}

const (
	pname = "cl-release"

	timeLayout = "2006-01-02 15:04:05 MST"
)

var (
	environ Cfg
)

type requiredUsage struct {
	cmd         *cobra.Command
	msg         string
	explanation string
}

func (e requiredUsage) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return "More information needed"
}

func silenceUsage(cmd *cobra.Command, args []string) {
	// If we set this when creating cmd, then if cobra fails argument
	// validation, it doesn't emit the usage, but if we leave it alone, we
	// get a usage message on all errors.  Here, we set it after all the
	// argument validation, and we get a usage message only on argument
	// validation failure.
	// See https://github.com/spf13/cobra/issues/340#issuecomment-378726225
	cmd.SilenceUsage = true
}

func processEnv() error {
	if err := envcfg.Unmarshal(&environ); err != nil {
		return err
	}

	if environ.PostgresConnection == "" {
		if environ.BackupPostgresConnection == "" {
			return fmt.Errorf("B10E_CLRELEASE_POSTGRES_CONNECTION " +
				"or REG_DBURI must be set")
		}
		environ.PostgresConnection = environ.BackupPostgresConnection
	}

	return nil
}

func makeApplianceDB(postgresURI string) (appliancedb.DataStore, error) {
	postgresURI, err := pgutils.PasswordPrompt(postgresURI)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get DB password")
	}
	applianceDB, err := appliancedb.Connect(postgresURI)
	if err != nil {
		return nil, errors.Wrap(err, "failed to connect to DB")
	}
	err = applianceDB.Ping()
	if err != nil {
		return nil, errors.Wrap(err, "failed to ping DB")
	}
	return applianceDB, nil
}

func getConfigClientHandle(cuuid string) (*cfgapi.Handle, error) {
	configd, err := clcfg.NewConfigd(pname, cuuid,
		environ.ConfigdConnection, !environ.ConfigdDisableTLS)
	if err != nil {
		return nil, err
	}
	configHandle := cfgapi.NewHandle(configd)
	return configHandle, nil
}

func createRelease(cmd *cobra.Command, args []string) error {
	platform, _ := cmd.Flags().GetString("platform")
	prefix, _ := cmd.Flags().GetString("prefix")
	// stream, _ := cmd.Flags().GetString("stream")
	// succeeds, _ := cmd.Flags().GetStringArray("succeeds")

	if platform == "" {
		platform = environ.Platform
		if platform == "" {
			return fmt.Errorf("platform must be one of x86, rpi3, mt7623")
		}
	}
	if prefix == "" {
		prefix = environ.Prefix
		if prefix == "" {
			prefix = release.DefaultArtifactPrefix
		}
	}

	repoCommits := make(map[string]string, len(args)-1)
	for _, arg := range args[1:] {
		s := strings.Split(arg, ":")
		if len(s) != 2 {
			return fmt.Errorf("arg in incorrect format: %s", arg)
		}
		if _, ok := repoCommits[s[0]]; ok {
			return fmt.Errorf("repo %s specified more than once", s[0])
		}
		repoCommits[s[0]] = s[1]
	}

	db, err := makeApplianceDB(environ.PostgresConnection)
	if err != nil {
		return err
	}
	defer db.Close()

	rel, err := release.CreateRelease(db, prefix, platform, args[0], repoCommits)
	if err != nil {
		return err
	}

	b, err := json.MarshalIndent(rel, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func listRelease(cmd *cobra.Command, args []string) error {
	db, err := makeApplianceDB(environ.PostgresConnection)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx := context.Background()
	releases, err := db.ListReleases(ctx)
	switch err.(type) {
	case nil:
	case appliancedb.BadReleaseError:
		fmt.Println(err)
	default:
		return err
	}

	table, _ := prettytable.NewTable(
		prettytable.Column{Header: "UUID"},
		prettytable.Column{Header: "Created At"},
		prettytable.Column{Header: "Name"},
		prettytable.Column{Header: "Platform"},
		prettytable.Column{Header: "Commits"},
	)
	table.Separator = "  "

	for _, release := range releases {
		ca := make([]string, len(release.Commits))
		for i, commit := range release.Commits {
			hash := hex.EncodeToString(commit.Commit)[:7]
			ca[i] = fmt.Sprintf("%s:%s", commit.Repo, hash)
		}
		table.AddRow(release.UUID,
			release.Creation.In(time.Local).Round(time.Second).
				Format(timeLayout),
			release.Metadata["name"], release.Platform,
			strings.Join(ca, " "))
	}
	table.Print()

	return nil
}

func showRelease(cmd *cobra.Command, args []string) error {
	// jsonP, _ := cmd.Flags().GetBool("json")

	// The argument we get is the release UUID.  We could try harder to
	// figure it out based on other inputs (platform and name, commit, etc).
	relUU, err := uuid.FromString(args[0])
	if err != nil {
		return err
	}

	db, err := makeApplianceDB(environ.PostgresConnection)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx := context.Background()
	dbRel, err := db.GetRelease(ctx, relUU)
	if err != nil {
		return err
	}

	rel := release.FromDBRelease(dbRel)
	b, err := json.MarshalIndent(rel, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))

	return nil
}

func applianceStatus(cmd *cobra.Command, args []string) error {
	appUUStrs, _ := cmd.Flags().GetStringArray("app")
	siteUUStrs, _ := cmd.Flags().GetStringArray("site")
	orgUUStrs, _ := cmd.Flags().GetStringArray("org")
	noNames, _ := cmd.Flags().GetBool("no-name")
	verbose, _ := cmd.Flags().GetBool("verbose")

	if (len(appUUStrs) > 0 && len(siteUUStrs) > 0) ||
		(len(appUUStrs) > 0 && len(orgUUStrs) > 0) ||
		(len(siteUUStrs) > 0 && len(orgUUStrs) > 0) {
		return fmt.Errorf("Only one of --app, --site, or --org may be specified")
	}

	db, err := makeApplianceDB(environ.PostgresConnection)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx := context.Background()

	// If we didn't specify --app, --site, or --org, then dump everything,
	// per-appliance, represented by an empty appUUs.  We take advantage of
	// the constraint that only one of appUUStrs, siteUUStrs, and orgUUStrs
	// is populated being enforced above.
	appUUs := make([]uuid.UUID, len(appUUStrs))
	for i, appUUStr := range appUUStrs {
		appUU, err := uuid.FromString(appUUStr)
		if err != nil {
			return err
		}
		appUUs[i] = appUU
	}

	for _, siteUUStr := range siteUUStrs {
		fm, err := registry.SiteUUIDByNameFuzzy(ctx, db, siteUUStr)
		if err != nil {
			if ase, ok := err.(registry.AmbiguousSiteError); ok {
				return errors.New(strings.TrimSpace(ase.Pretty()))
			}
			return err
		}
		if fm.Name != "" {
			fmt.Fprintf(os.Stderr,
				"%q matched more than one site, but %q (%s) "+
					"seemed the most likely\n",
				siteUUStr, fm.Name, fm.UUID)
		}
		var siteUU uuid.UUID
		if fm.UUID != uuid.Nil {
			siteUU = fm.UUID
		}
		siteApps, err := db.ApplianceIDsBySiteID(ctx, siteUU)
		if err != nil {
			return err
		}
		for _, appID := range siteApps {
			appUUs = append(appUUs, appID.ApplianceUUID)
		}
	}

	for _, orgUUStr := range orgUUStrs {
		fm, err := registry.OrgUUIDByNameFuzzy(ctx, db, orgUUStr)
		if err != nil {
			if aoe, ok := err.(registry.AmbiguousOrgError); ok {
				return errors.New(strings.TrimSpace(aoe.Pretty()))
			}
			return err
		}
		if fm.Name != "" {
			fmt.Fprintf(os.Stderr,
				"%q matched more than one org, but %q (%s) "+
					"seemed the most likely\n",
				orgUUStr, fm.Name, fm.UUID)
		}
		var orgUU uuid.UUID
		if fm.UUID != uuid.Nil {
			orgUU = fm.UUID
		}
		orgApps, err := db.ApplianceIDsByOrgID(ctx, orgUU)
		if err != nil {
			return err
		}
		for _, appID := range orgApps {
			appUUs = append(appUUs, appID.ApplianceUUID)
		}
	}

	status, err := db.GetReleaseStatusByAppliances(ctx, appUUs)
	if err != nil {
		return err
	}

	var asoMap map[uuid.UUID]appliancedb.AppSiteOrg
	if !noNames {
		chain, err := db.AppSiteOrgChain(ctx, appUUs)
		if err != nil {
			return err
		}
		asoMap = make(map[uuid.UUID]appliancedb.AppSiteOrg, len(appUUs))
		for _, aso := range chain {
			asoMap[aso.AppUUID] = aso
		}
	}

	var columns []prettytable.Column
	if noNames {
		columns = append(columns, prettytable.Column{Header: "Appliance"})
	} else {
		columns = append(columns, prettytable.Column{Header: "Organization/Site"})
		columns = append(columns, prettytable.Column{Header: "Appliance"})
	}
	// Checkmark for up-to-date?
	columns = append(columns,
		prettytable.Column{Header: "Target Release UUID"},
		prettytable.Column{Header: "Name"},
		prettytable.Column{Header: "Current Release UUID"},
		prettytable.Column{Header: "Name"},
		prettytable.Column{Header: "Prog"}, // ress
		prettytable.Column{Header: "Since"})
	table, _ := prettytable.NewTable(columns...)
	table.Separator = "  "

	appUUs = make([]uuid.UUID, 0)
	for appUU := range status {
		appUUs = append(appUUs, appUU)
	}
	appNames := make(map[uuid.UUID][]interface{}, len(appUUs))
	if noNames {
		for _, appUU := range appUUs {
			appNames[appUU] = []interface{}{appUU.String()}
		}
		sort.Slice(appUUs, func(i, j int) bool {
			return bytes.Compare(appUUs[i].Bytes(), appUUs[j].Bytes()) == -1
		})
	} else {
		for _, appUU := range appUUs {
			appNames[appUU] = []interface{}{
				fmt.Sprintf("%s / %s", asoMap[appUU].OrgName, asoMap[appUU].SiteName),
				asoMap[appUU].AppName,
			}
		}
		sort.Slice(appUUs, func(i, j int) bool {
			ei0 := appNames[appUUs[i]][0].(string)
			ei1 := appNames[appUUs[i]][1].(string)
			ej0 := appNames[appUUs[j]][0].(string)
			ej1 := appNames[appUUs[j]][1].(string)
			return ei0+ei1 < ej0+ej1
		})
	}

	var messages, logURLs []string
	for _, appUU := range appUUs {
		stat := status[appUU]
		var targUU, targName, curUU, curName, since, progress string

		// If .Valid is false, we want the empty string.
		messages = append(messages, stat.Message.String)
		logURLs = append(logURLs, stat.LogURL.String)

		if stat.TargetReleaseUUID.Valid {
			targUU = stat.TargetReleaseUUID.UUID.String()
			targName = stat.TargetReleaseName.String
		} else {
			targUU = "-"
			targName = "-"
		}

		if stat.CurrentReleaseUUID.Valid {
			if stat.CurrentReleaseUUID.UUID == uuid.Nil {
				var a []string
				for repo, hash := range stat.Commits {
					l := 10
					if len(hash) < l {
						l = len(hash)
					}
					a = append(a, repo+":"+hash[:l])
				}
				if len(a) > 0 {
					curUU = strings.Join(a, " ")
				} else {
					curUU = "-"
				}
				// We could try to figure out which
				// release the commits most closely
				// describe, then print that, plus its
				// diffs. (~Beta 2: PS:d4db33f)
			} else {
				curUU = stat.CurrentReleaseUUID.UUID.String()
			}
			curName = stat.CurrentReleaseName.String
			since = stat.RunningSince.Time.In(time.Local).
				Round(time.Second).Format(timeLayout)
		} else {
			curUU = "-"
			curName = "-"
			since = "-"
		}

		if stat.Stage.Valid {
			success := "-"
			if stat.Success.Valid {
				if stat.Success.Bool {
					success = "✔"
				} else {
					success = "✘"
				}
			}
			progress = map[string]string{
				"notified":           success + "---",
				"manifest_retrieved": "✔" + success + "--",
				"installed":          "✔✔" + success + "-",
				"complete":           "✔✔✔" + success,
			}[stat.Stage.String]
		}

		var outCols []interface{}
		outCols = append(outCols, appNames[appUU]...)
		outCols = append(outCols, targUU, targName, curUU, curName,
			progress, since)
		table.AddRow(outCols...)
	}

	lines := bytes.Split(bytes.TrimSpace(table.Bytes()), []byte("\n"))
	os.Stdout.Write(append(lines[0], []byte("\n")...))
	for i, line := range lines[1:] {
		os.Stdout.Write(append(line, []byte("\n")...))

		// Messages and log URLs are typically not found for
		// successfully completed upgrades.  These will normally only
		// show up when an upgrade has failed, or when an appliance is
		// in between stages.
		if verbose && messages[i] != "" {
			fmt.Printf("  Message: %s\n", strings.TrimSpace(messages[i]))
		}
		if verbose && logURLs[i] != "" {
			fmt.Printf("  Log URL: %s\n", logURLs[i])
		}

	}

	return nil
}

func notifyAppliances(cmd *cobra.Command, args []string) error {
	appUUStr, _ := cmd.Flags().GetString("app")
	if appUUStr == "" {
		return fmt.Errorf("Must specify appliance UUID with --app")
	}
	appUU, err := uuid.FromString(appUUStr)
	if err != nil {
		return err
	}

	relUU, err := uuid.FromString(args[0])
	if err != nil {
		return err
	}

	db, err := makeApplianceDB(environ.PostgresConnection)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx := context.Background()
	err = db.SetTargetRelease(ctx, appUU, relUU)
	if err != nil {
		return err
	}

	appID, err := db.ApplianceIDByUUID(ctx, appUU)
	if err != nil {
		return err
	}
	cfgHdl, err := getConfigClientHandle(appID.SiteUUID.String())
	if err != nil {
		return err
	}

	nodeID := appID.SystemReprHWSerial.ValueOrZero()
	if nodeID == "" {
		nodeID, _ = cmd.Flags().GetString("nodeid")
		if nodeID == "" {
			return fmt.Errorf("Appliance has no serial number; " +
				"must specify self-selected UUID with --nodeid")
		}
	}
	targetPath := fmt.Sprintf("@/nodes/%s/target_release", nodeID)
	ops := []cfgapi.PropertyOp{
		{Op: cfgapi.PropCreate, Name: targetPath, Value: relUU.String()},
	}
	cmdHdl := cfgHdl.Execute(ctx, ops)
	_, err = cmdHdl.Status(ctx)
	now := time.Now()
	if err != nil {
		if err != cfgapi.ErrQueued && err != cfgapi.ErrInProgress {
			db.SetUpgradeStage(ctx, appUU, relUU, now, "notified",
				false, err.Error())
			return err
		}
		fmt.Println("Notification has been queued; the appliance will " +
			"upgrade once it receives the notification")
	}
	db.SetUpgradeStage(ctx, appUU, relUU, now, "notified", true, "")

	return nil
}

func main() {
	rootCmd := cobra.Command{
		Use:              os.Args[0],
		PersistentPreRun: silenceUsage,
	}

	createCmd := &cobra.Command{
		Use:   "create [flags] name repo:commit ...",
		Short: "Create a new release",
		Args:  cobra.MinimumNArgs(2),
		RunE:  createRelease,
	}
	createCmd.Flags().String("platform", "", "platform")
	createCmd.Flags().String("prefix", "", "URL or path prefix to artifacts")
	createCmd.Flags().String("stream", "", "stream") // XXX Multiple defines a merge?
	createCmd.Flags().StringArray("succeeds", []string{}, "preceding release")
	rootCmd.AddCommand(createCmd)

	listCmd := &cobra.Command{
		Use:   "list [flags]",
		Short: "List releases",
		Args:  cobra.NoArgs,
		RunE:  listRelease,
	}
	// XXX Will want flags to filter the releases (uuid, platform, name, one
	// or more commit IDs, maybe date range?)
	rootCmd.AddCommand(listCmd)

	showCmd := &cobra.Command{
		Use:   "show [flags] <release>",
		Short: "Show details of a release",
		Args:  cobra.ExactArgs(1),
		RunE:  showRelease,
	}
	showCmd.Flags().BoolP("json", "j", false, "Print JSON release descriptor")
	rootCmd.AddCommand(showCmd)

	notifyCmd := &cobra.Command{
		Use:   "notify [flags] <release>",
		Short: "Notify appliances of release availability",
		Args:  cobra.ExactArgs(1),
		RunE:  notifyAppliances,
	}
	notifyCmd.Flags().StringP("app", "a", "", "appliance UUID")
	notifyCmd.Flags().StringP("nodeid", "N", "", "node ID (if not serial number)")
	rootCmd.AddCommand(notifyCmd)

	statusCmd := &cobra.Command{
		Use:   "status [flags]",
		Short: "Get release status of appliances",
		Long: `Get the release status of appliances.

The 'Prog' column indicates success (✔) or failure (✘) of each stage.
The stages, in order, are 'notified' (a notification has been posted to
the config tree), 'manifest retrieved' (the appliance has retrieved the
manifest), 'installed' (the appliance has performed its installation and
sent the log to the cloud), 'complete' (the appliance has rebooted and
reported its running release).`,
		Args: cobra.NoArgs,
		RunE: applianceStatus,
	}
	statusCmd.Flags().StringArrayP("app", "a", []string{}, "appliance UUIDs")
	statusCmd.Flags().StringArrayP("site", "s", []string{}, "site UUIDs")
	statusCmd.Flags().StringArrayP("org", "o", []string{}, "organization UUIDs")
	statusCmd.Flags().BoolP("no-name", "n", false, "don't resolve appliance UUIDs into names")
	statusCmd.Flags().BoolP("verbose", "v", false, "extra output (latest message and log URL)")
	rootCmd.AddCommand(statusCmd)

	if err := processEnv(); err != nil {
		fmt.Printf("Environment Error: %s\n", err)
		return
	}

	err := rootCmd.Execute()
	if err, ok := err.(requiredUsage); ok {
		err.cmd.Usage()
		if err.explanation != "" {
			extraUsage := "\n" + err.explanation
			io.WriteString(err.cmd.OutOrStderr(), extraUsage)
		}
		os.Exit(2)
	}
	os.Exit(map[bool]int{true: 0, false: 1}[err == nil])
}

