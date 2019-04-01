/*
 * COPYRIGHT 2019 Brightgate Inc.  All rights reserved.
 *
 * This copyright notice is Copyright Management Information under 17 USC 1202
 * and is included to protect this work and deter copyright infringement.
 * Removal or alteration of this Copyright Management Information without the
 * express written permission of Brightgate Inc is prohibited, and any
 * such unauthorized removal or alteration will be a violation of federal law.
 */

package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"

	"github.com/satori/uuid"
	"github.com/spf13/cobra"
	"github.com/tatsushid/go-prettytable"
	"github.com/tomazk/envcfg"

	"bg/cl_common/pgutils"
	"bg/cl_common/registry"
	"bg/cloud_models/appliancedb"
)

const pname = "cl-reg"

var environ struct {
	PostgresConnection string `envcfg:"REG_DBURI"`
	Project            string `envcfg:"REG_PROJECT_ID"`
	Region             string `envcfg:"REG_REGION_ID"`
	Registry           string `envcfg:"REG_REGISTRY_ID"`
	ConfigdConnection  string `envcfg:"B10E_CLREG_CLCONFIGD_CONNECTION"`
	DisableTLS         bool   `envcfg:"B10E_CLREG_DISABLE_TLS"`
	AccountSecret      string `envcfg:"B10E_CLREG_ACCOUNT_SECRET"`
}

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

func readJSON(path string) (*registry.ApplianceRegistry, error) {
	var reg registry.ApplianceRegistry
	if path == "" {
		return &reg, nil
	}

	jblob, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	err = json.Unmarshal(jblob, &reg)
	if err != nil {
		return nil, err
	}

	return &reg, nil
}

func first(opts ...string) string {
	for _, opt := range opts {
		if opt != "" {
			return opt
		}
	}
	return ""
}

func assembleRegistry(cmd *cobra.Command) (appliancedb.DataStore, *registry.ApplianceRegistry, error) {
	var reg registry.ApplianceRegistry
	project, _ := cmd.Flags().GetString("project")
	region, _ := cmd.Flags().GetString("region")
	regID, _ := cmd.Flags().GetString("registry")
	inputPath, _ := cmd.Flags().GetString("input")

	fileReg, err := readJSON(inputPath)
	if err != nil {
		return nil, nil, err
	}
	// This means there is no way to override a non-empty parameter from the
	// environment or the JSON file with, say, `-p ""`.
	reg.Project = first(project, fileReg.Project, environ.Project)
	reg.Region = first(region, fileReg.Region, environ.Region)
	reg.Registry = first(regID, fileReg.Registry, environ.Registry)

	pgconn := first(fileReg.DbURI, environ.PostgresConnection)
	if pgconn == "" {
		return nil, nil, requiredUsage{
			cmd: cmd,
			msg: "Missing database URI",
			explanation: "You must provide the registry database URI through the environment\n" +
				"variable REG_DBURI or via the JSON file specified with -i.\n",
		}
	}
	pgconn, err = pgutils.PasswordPrompt(pgconn)
	if err != nil {
		return nil, nil, err
	}
	reg.DbURI = pgconn
	db, err := appliancedb.Connect(reg.DbURI)
	if err != nil {
		return nil, nil, err
	}
	return db, &reg, nil
}

func newOrg(cmd *cobra.Command, args []string) error {
	orgName := args[0]

	db, _, err := assembleRegistry(cmd)
	if err != nil {
		return err
	}
	defer db.Close()

	orgUU, err := registry.NewOrganization(context.Background(), db, orgName)
	if err != nil {
		return err
	}
	fmt.Printf("Created Org: uuid=%s, name='%s'\n", orgUU, orgName)
	return nil
}

func listOrgs(cmd *cobra.Command, args []string) error {
	db, _, err := assembleRegistry(cmd)
	if err != nil {
		return err
	}
	defer db.Close()

	orgs, err := db.AllOrganizations(context.Background())
	if err != nil {
		return err
	}

	table, _ := prettytable.NewTable(
		prettytable.Column{Header: "UUID"},
		prettytable.Column{Header: "Name"},
	)
	table.Separator = "  "

	for _, org := range orgs {
		table.AddRow(org.UUID, org.Name)
	}
	table.Print()
	return nil
}

func newOAuth2OrgRule(cmd *cobra.Command, args []string) error {
	provider := args[0]
	ruleType := appliancedb.OAuth2OrgRuleType(args[1])
	ruleValue := args[2]
	organization := args[3]
	orgUU := uuid.Must(uuid.FromString(organization))

	if ruleType != appliancedb.RuleTypeTenant &&
		ruleType != appliancedb.RuleTypeDomain &&
		ruleType != appliancedb.RuleTypeEmail {
		return fmt.Errorf("Invalid rule type %q; use 'tenant', 'domain', or 'email'", ruleType)
	}

	db, _, err := assembleRegistry(cmd)
	if err != nil {
		return err
	}
	defer db.Close()

	err = registry.NewOAuth2OrganizationRule(context.Background(), db, provider,
		ruleType, ruleValue, orgUU)
	if err != nil {
		return err
	}
	fmt.Printf("Created OAuth2OrgRule: provider=%q, ruleType=%q ruleValue=%q, org=%q\n",
		provider, ruleType, ruleValue, orgUU)
	return nil
}

func listOAuth2OrgRules(cmd *cobra.Command, args []string) error {
	db, _, err := assembleRegistry(cmd)
	if err != nil {
		return err
	}
	defer db.Close()

	rules, err := db.AllOAuth2OrganizationRules(context.Background())
	if err != nil {
		return err
	}

	table, _ := prettytable.NewTable(
		prettytable.Column{Header: "Provider"},
		prettytable.Column{Header: "RuleType"},
		prettytable.Column{Header: "RuleValue"},
		prettytable.Column{Header: "OrganizationUUID"},
	)
	table.Separator = "  "

	for _, rule := range rules {
		_ = table.AddRow(rule.Provider, string(rule.RuleType),
			rule.RuleValue, rule.OrganizationUUID.String())
	}
	table.Print()
	return nil
}

func newSite(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	siteName := args[0]
	orgUUID := uuid.Must(uuid.FromString(args[1]))

	db, _, err := assembleRegistry(cmd)
	if err != nil {
		return err
	}
	defer db.Close()

	as, err := hex.DecodeString(environ.AccountSecret)
	if err != nil {
		return err
	}
	db.AccountSecretsSetPassphrase(as)

	siteUU, err := registry.NewSite(ctx, db, siteName, orgUUID)
	if err != nil {
		return err
	}
	fmt.Printf("Created Site: uuid=%s, name='%s' organization='%s'\n", siteUU, siteName, orgUUID)

	if orgUUID == appliancedb.NullOrganizationUUID {
		fmt.Printf("Warning: null organization; usually for testing only\n")
		return nil
	}

	site, err := db.CustomerSiteByUUID(ctx, siteUU)
	if err != nil {
		return err
	}

	accounts, err := db.AccountsByOrganization(ctx, orgUUID)
	if err != nil {
		return err
	}

	fmt.Printf("Syncing accounts:\n")
	for _, acct := range accounts {
		err = registry.SyncAccountSelfProv(ctx, db, getConfig, acct.UUID,
			[]appliancedb.CustomerSite{*site})
		if err != nil {
			fmt.Printf("  Sync Error <%s>: %v\n", acct.Email, err)
		} else {
			fmt.Printf("  Sync    OK <%s>\n", acct.Email)
		}
	}
	return nil
}

func listSites(cmd *cobra.Command, args []string) error {
	db, _, err := assembleRegistry(cmd)
	if err != nil {
		return err
	}
	defer db.Close()

	sites, err := db.AllCustomerSites(context.Background())
	if err != nil {
		return err
	}

	table, _ := prettytable.NewTable(
		prettytable.Column{Header: "UUID"},
		prettytable.Column{Header: "OrganizationUUID"},
		prettytable.Column{Header: "Name"},
	)
	table.Separator = "  "

	for _, site := range sites {
		table.AddRow(site.UUID, site.OrganizationUUID, site.Name)
	}
	table.Print()
	return nil
}

func listAppliances(cmd *cobra.Command, args []string) error {
	appID, _ := cmd.Flags().GetString("name")
	siteUUID, _ := cmd.Flags().GetString("site-uuid")

	db, reg, err := assembleRegistry(cmd)
	if err != nil {
		return err
	}

	apps, err := db.AllApplianceIDs(context.Background())
	if err != nil {
		return err
	}

	// XXX We could write a query with a WHERE clause ...
	// XXX It might also be nice to have pattern matching.
	// XXX And sorting
	matchingApps := make([]appliancedb.ApplianceID, 0)
	for _, app := range apps {
		if (reg.Project == "" || reg.Project == app.GCPProject) &&
			(reg.Region == "" || reg.Region == app.GCPRegion) &&
			(reg.Registry == "" || reg.Registry == app.ApplianceReg) &&
			(siteUUID == "" || app.SiteUUID.String() == siteUUID) &&
			(appID == "" || appID == app.ApplianceRegID) {
			matchingApps = append(matchingApps, app)
		}
	}

	if len(matchingApps) == 0 {
		return nil
	}

	table, _ := prettytable.NewTable(
		prettytable.Column{Header: "UUID"},
		prettytable.Column{Header: "Site"},
		prettytable.Column{Header: "Project"},
		prettytable.Column{Header: "Region"},
		prettytable.Column{Header: "Registry"},
		prettytable.Column{Header: "Appliance Name"},
	)
	table.Separator = "  "

	for _, app := range matchingApps {
		table.AddRow(app.ApplianceUUID, app.SiteUUID,
			app.GCPProject, app.GCPRegion,
			app.ApplianceReg, app.ApplianceRegID)
	}
	table.Print()
	return nil
}

func newAppliance(cmd *cobra.Command, args []string) error {
	appID := args[0]
	outdir, _ := cmd.Flags().GetString("directory")
	appUUID, _ := cmd.Flags().GetString("uuid")
	siteUUID, _ := cmd.Flags().GetString("site-uuid")

	var appUU uuid.UUID
	if appUUID != "" {
		var err error
		if appUU, err = uuid.FromString(appUUID); err != nil {
			return err
		}
	} else {
		appUU = uuid.NewV4()
	}

	// nil siteUU means "pick me a siteid"
	var siteUU *uuid.UUID
	if siteUUID != "" {
		var u uuid.UUID
		var err error
		if u, err = uuid.FromString(siteUUID); err != nil {
			return err
		}
		siteUU = &u
	}

	db, reg, err := assembleRegistry(cmd)
	if err != nil {
		return err
	}
	defer db.Close()

	var keyPEM []byte
	var resultSiteUU uuid.UUID
	appUU, resultSiteUU, keyPEM, _, err = registry.NewAppliance(context.Background(),
		db, appUU, siteUU, reg.Project, reg.Region, reg.Registry, appID)
	if err != nil {
		return err
	}

	jmap := map[string]string{
		"project":      reg.Project,
		"region":       reg.Region,
		"registry":     reg.Registry,
		"appliance_id": appID,
		"private_key":  string(keyPEM),
	}
	jout, err := json.MarshalIndent(jmap, "", "\t")
	if err != nil {
		return err
	}

	var ioerr error
	var secretsFile string
	if ioerr = os.MkdirAll(outdir, 0700); ioerr == nil {
		secretsFile = outdir + "/" + appID + ".cloud.secret.json"
		ioerr = ioutil.WriteFile(secretsFile, jout, 0600)
	}

	fmt.Printf("-------------------------------------------------------------\n")
	fmt.Printf("Created device: projects/%s/locations/%s/registries/%s/appliances/%s\n",
		reg.Project, reg.Region, reg.Registry, appID)
	fmt.Printf("     Site UUID: %s\n", resultSiteUU)
	fmt.Printf("Appliance UUID: %s\n", appUU)
	if ioerr == nil {
		fmt.Printf("  Secrets file: %s\n", secretsFile)
		fmt.Printf("-------------------------------------------------------------\n")
		fmt.Printf("Next, provision %s to the appliance at:\n", secretsFile)
		fmt.Printf("    /data/secret/rpcd/cloud.secret.json\n")
		fmt.Printf("    /var/spool/secret/rpcd/cloud.secret.json (on Debian)\n")
	} else {
		fmt.Printf("-------------------------------------------------------------\n")
		fmt.Printf("Secrets file couldn't be written: %s\n", ioerr)
		fmt.Printf("Copy the following to the appliance at:\n")
		fmt.Printf("    /data/secret/rpcd/cloud.secret.json\n")
		fmt.Printf("    /var/spool/secret/rpcd/cloud.secret.json (on Debian)\n")
		fmt.Printf("%s\n", jout)
	}

	return err
}

func main() {
	rootCmd := &cobra.Command{
		Use:              os.Args[0],
		PersistentPreRun: silenceUsage,
	}

	orgCmd := &cobra.Command{
		Use:   "org <subcmd> [flags] [args]",
		Short: "Administer organizations in the registry",
		Args:  cobra.NoArgs,
	}
	rootCmd.AddCommand(orgCmd)

	newOrgCmd := &cobra.Command{
		Use:   "new [flags] <org name>",
		Args:  cobra.ExactArgs(1),
		Short: "Create an org and add it to the registry",
		RunE:  newOrg,
	}
	newOrgCmd.Flags().StringP("input", "i", "", "registry data JSON file")
	orgCmd.AddCommand(newOrgCmd)

	listOrgCmd := &cobra.Command{
		Use:   "list",
		Args:  cobra.NoArgs,
		Short: "List organizations in the registry",
		RunE:  listOrgs,
	}
	listOrgCmd.Flags().StringP("input", "i", "", "registry data JSON file")
	orgCmd.AddCommand(listOrgCmd)

	siteCmd := &cobra.Command{
		Use:   "site <subcmd> [flags] [args]",
		Short: "Administer sites in the registry",
		Args:  cobra.NoArgs,
	}
	rootCmd.AddCommand(siteCmd)

	newSiteCmd := &cobra.Command{
		Use:   "new [flags] <site name> <organization-uuid>",
		Args:  cobra.ExactArgs(2),
		Short: "Create a site and add it to the registry",
		RunE:  newSite,
	}
	newSiteCmd.Flags().StringP("input", "i", "", "registry data JSON file")
	siteCmd.AddCommand(newSiteCmd)

	listSiteCmd := &cobra.Command{
		Use:   "list",
		Args:  cobra.NoArgs,
		Short: "List sites in the registry",
		RunE:  listSites,
	}
	listSiteCmd.Flags().StringP("input", "i", "", "registry data JSON file")
	siteCmd.AddCommand(listSiteCmd)

	oauth2OrgRuleCmd := &cobra.Command{
		Use:   "oauth2_org_rule <subcmd> [flags] [args]",
		Short: "Administer OAuth2OrgRules in the registry",
		Args:  cobra.NoArgs,
	}
	rootCmd.AddCommand(oauth2OrgRuleCmd)

	newOAuth2OrgRuleCmd := &cobra.Command{
		Use:   "new [flags] <provider> [tenant|domain|email] <value> <organization-uuid>",
		Args:  cobra.ExactArgs(4),
		Short: "Create an OAuth2OrgRule and add it to the registry",
		RunE:  newOAuth2OrgRule,
	}
	newOAuth2OrgRuleCmd.Flags().StringP("input", "i", "", "registry data JSON file")
	oauth2OrgRuleCmd.AddCommand(newOAuth2OrgRuleCmd)

	listOAuth2OrgRuleCmd := &cobra.Command{
		Use:   "list",
		Args:  cobra.NoArgs,
		Short: "List OAuth2OrgRules in the registry",
		RunE:  listOAuth2OrgRules,
	}
	listOAuth2OrgRuleCmd.Flags().StringP("input", "i", "", "registry data JSON file")
	oauth2OrgRuleCmd.AddCommand(listOAuth2OrgRuleCmd)

	appCmd := &cobra.Command{
		Use:   "app <subcmd> [flags] [args]",
		Short: "Administer appliances in the registry",
		Args:  cobra.NoArgs,
	}
	rootCmd.AddCommand(appCmd)

	newAppCmd := &cobra.Command{
		Use:   "new [flags] <appliance name>",
		Args:  cobra.ExactArgs(1),
		Short: "Create an appliance and add it to the registry",
		RunE:  newAppliance,
	}
	newAppCmd.Flags().StringP("directory", "d", ".", "output directory")
	newAppCmd.Flags().StringP("project", "p", "", "GCP project")
	newAppCmd.Flags().StringP("region", "R", "", "GCP region")
	newAppCmd.Flags().StringP("registry", "r", "", "appliance registry")
	newAppCmd.Flags().StringP("input", "i", "", "registry data JSON file")
	newAppCmd.Flags().StringP("uuid", "u", "", "appliance UUID")
	newAppCmd.Flags().StringP("site-uuid", "s", "", "site UUID")
	appCmd.AddCommand(newAppCmd)

	listAppCmd := &cobra.Command{
		Use:     "list [flags]",
		Args:    cobra.NoArgs,
		Short:   "List appliances in the registry",
		Aliases: []string{"ls"},
		RunE:    listAppliances,
	}
	listAppCmd.Flags().StringP("project", "p", "", "GCP project")
	listAppCmd.Flags().StringP("region", "R", "", "GCP region")
	listAppCmd.Flags().StringP("registry", "r", "", "appliance registry")
	listAppCmd.Flags().StringP("name", "n", "", "appliance name")
	listAppCmd.Flags().StringP("input", "i", "", "registry data JSON file")
	listAppCmd.Flags().StringP("site-uuid", "s", "", "site UUID")
	appCmd.AddCommand(listAppCmd)

	cqMain(rootCmd)
	accountMain(rootCmd)

	if err := envcfg.Unmarshal(&environ); err != nil {
		fmt.Printf("Environment Error: %s", err)
		return
	}

	err := rootCmd.Execute()
	if err, ok := err.(requiredUsage); ok {
		err.cmd.Usage()
		extraUsage := "\n" + err.explanation
		io.WriteString(err.cmd.OutOrStderr(), extraUsage)
	}
	os.Exit(map[bool]int{true: 0, false: 1}[err == nil])
}
