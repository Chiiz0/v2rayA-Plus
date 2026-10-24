// v2raya-db operates on explicit offline database paths. Keep this command
// independent of the service's configuration, DNS and process initialization.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/v2rayA/v2rayA/db/backup"
)

const usage = `Usage:
  v2raya-db export --db SOURCE --out ARCHIVE [--lock-timeout 3s]
  v2raya-db verify --in ARCHIVE [--db SOURCE] [--docker] [--lock-timeout 3s]
  v2raya-db restore --in ARCHIVE --db NEW_DATABASE
  v2raya-db dns-backup --config-dir SOURCE_DIRECTORY --out NEW_FILE

This tool supports Linux. Use a stopped installation or a consistent offline copy.
Existing output files
are never overwritten. Database archives contain credentials and are written
with mode 0600. DNS recovery files and Docker volumes are separate from the
database archive. The dns-backup command copies recovery data without applying it.

verify --db compares every stored byte and bucket sequence with the archive.
verify --docker explicitly enables read-only Docker metadata checks; it does not
validate protocol settings inside container volumes or recreate containers.

Exit codes: 0 success, 1 operational failure, 2 invalid arguments or audit failure.
An export with audit failures still saves all original bytes for recovery.
`

type comparison struct {
	Match         bool   `json:"match"`
	LogicalSHA256 string `json:"databaseLogicalSHA256"`
}

type commandReport struct {
	Operation  string                 `json:"operation"`
	Success    bool                   `json:"success"`
	Archive    string                 `json:"archive,omitempty"`
	Database   string                 `json:"database,omitempty"`
	Manifest   *backup.Manifest       `json:"manifest,omitempty"`
	Audit      *backup.Report         `json:"audit,omitempty"`
	Comparison *comparison            `json:"comparison,omitempty"`
	DNS        *backup.DNSReport      `json:"dnsRecovery,omitempty"`
	External   *backup.ExternalReport `json:"docker,omitempty"`
	Notes      []string               `json:"notes,omitempty"`
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	operation := args[0]
	flags := flag.NewFlagSet(operation, flag.ContinueOnError)
	flags.SetOutput(stderr)
	var input, output, database, directory string
	var docker bool
	var timeout time.Duration
	switch operation {
	case "export":
		flags.StringVar(&database, "db", "", "existing offline Bolt database")
		flags.StringVar(&output, "out", "", "new archive file")
		flags.DurationVar(&timeout, "lock-timeout", 3*time.Second, "maximum database lock wait")
	case "verify":
		flags.StringVar(&input, "in", "", "archive to verify")
		flags.StringVar(&database, "db", "", "optional offline database to compare")
		flags.BoolVar(&docker, "docker", false, "inspect external Docker metadata (read-only)")
		flags.DurationVar(&timeout, "lock-timeout", 3*time.Second, "maximum database lock wait")
	case "restore":
		flags.StringVar(&input, "in", "", "archive to restore")
		flags.StringVar(&database, "db", "", "new destination database; must not exist")
	case "dns-backup":
		flags.StringVar(&directory, "config-dir", "", "source directory containing DNS recovery data")
		flags.StringVar(&output, "out", "", "new, separate copy of DNS recovery data")
	default:
		fmt.Fprintf(stderr, "unknown command %q\n%s", operation, usage)
		return 2
	}
	if err := flags.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 ||
		operation == "export" && (database == "" || output == "") ||
		operation == "verify" && input == "" ||
		operation == "restore" && (input == "" || database == "") ||
		operation == "dns-backup" && (directory == "" || output == "") {
		fmt.Fprint(stderr, "all paths must be provided explicitly\n", usage)
		return 2
	}
	if (operation == "export" || operation == "verify") && (timeout <= 0 || timeout > time.Minute) {
		fmt.Fprintln(stderr, "lock-timeout must be greater than zero and at most 1m")
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "v2raya-db: %v\n", err)
		return 1
	}
	emit := func(report commandReport) int {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return fail(err)
		}
		if !report.Success {
			return 2
		}
		return 0
	}
	if operation == "dns-backup" {
		dns, err := backup.CopyDNSRecovery(directory, output)
		if err != nil {
			return fail(err)
		}
		note := "Recovery data is copied separately and has not been applied to DNS. Keep it with the original host's backup."
		if !dns.Present {
			note = "No DNS recovery file exists in the source directory; no output was created."
		}
		return emit(commandReport{Operation: operation, Success: true, DNS: &dns, Notes: []string{note}})
	}
	if operation == "export" {
		archive, err := backup.Export(database, timeout)
		if err != nil {
			return fail(err)
		}
		if err := backup.Write(output, archive); err != nil {
			return fail(err)
		}
		audit := backup.Audit(archive)
		report := commandReport{Operation: operation, Success: !audit.HasErrors(), Archive: output, Database: database, Manifest: &archive.Manifest, Audit: &audit}
		report.Notes = []string{"All stored buckets, keys and values were saved, including unknown data. Docker volume contents are not part of this archive."}
		dns, err := backup.InspectDNSRecovery(filepath.Dir(database))
		if err != nil {
			report.Notes = append(report.Notes, "DNS recovery file inspection failed; check the source directory separately.")
		} else {
			report.DNS = &dns
			if dns.Present {
				report.Notes = append(report.Notes, "DNS recovery data exists beside the database. Use dns-backup to save it separately.")
			}
		}
		if audit.HasErrors() {
			report.Notes = append(report.Notes, "The archive was saved, but application data has audit errors. Restore is blocked until these are resolved.")
		}
		return emit(report)
	}
	archive, err := backup.Read(input)
	if err != nil {
		return fail(err)
	}
	audit := backup.Audit(archive)
	report := commandReport{Operation: operation, Success: !audit.HasErrors(), Archive: input, Manifest: &archive.Manifest, Audit: &audit}
	if operation == "verify" {
		if database != "" {
			actual, err := backup.Export(database, timeout)
			if err != nil {
				return fail(err)
			}
			report.Database = database
			report.Comparison = &comparison{Match: actual.Manifest.LogicalSHA256 == archive.Manifest.LogicalSHA256, LogicalSHA256: actual.Manifest.LogicalSHA256}
			report.Success = report.Success && report.Comparison.Match
		}
		if docker && !audit.HasErrors() {
			external := backup.InspectDocker(context.Background(), audit.Docker)
			report.External = &external
			report.Success = report.Success && !external.HasErrors()
		} else if docker {
			report.Notes = append(report.Notes, "Docker inspection was skipped because stored application data has audit errors.")
		}
		return emit(report)
	}
	if audit.HasErrors() {
		report.Notes = []string{"Restore was refused because application data has audit errors; no destination was created."}
		return emit(report)
	}
	if err := backup.Restore(database, archive); err != nil {
		return fail(err)
	}
	report.Database = database
	report.Notes = []string{
		"Restored to a new offline database and verified every stored value and bucket sequence.",
		"Stored runtime flags are preserved. No service, DNS recovery, container or volume was started or restored.",
	}
	return emit(report)
}
