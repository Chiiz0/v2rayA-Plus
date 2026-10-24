package main

import (
	"fmt"

	"github.com/v2rayA/v2rayA/db/backup"
	"github.com/v2rayA/v2rayA/db/runtimebackend"
)

type runtimeCommandReport struct {
	Operation string                 `json:"operation"`
	Success   bool                   `json:"success"`
	Runtime   *runtimebackend.Result `json:"runtime,omitempty"`
	Archive   string                 `json:"archive,omitempty"`
	Manifest  *backup.Manifest       `json:"manifest,omitempty"`
	Audit     *backup.Report         `json:"audit,omitempty"`
	Notes     []string               `json:"notes,omitempty"`
}

func isRuntimeOperation(operation string) bool {
	switch operation {
	case "runtime-migrate", "runtime-init", "runtime-restore", "runtime-rollback", "runtime-export", "runtime-verify":
		return true
	default:
		return false
	}
}

func runRuntime(operation, directory, input, output string) (runtimeCommandReport, error) {
	report := runtimeCommandReport{Operation: operation, Success: true}
	report.Notes = []string{"This offline operation uses the installation's instance lock. No service, DNS recovery or Docker resource was started."}
	if operation == "runtime-export" {
		archive, err := runtimebackend.Export(directory)
		if err != nil {
			return report, err
		}
		if err := backup.Write(output, archive); err != nil {
			return report, err
		}
		audit := backup.Audit(archive)
		report.Archive, report.Manifest, report.Audit = output, &archive.Manifest, &audit
		report.Success = !audit.HasErrors()
		report.Notes = append(report.Notes, "The archive preserves the complete current logical tree. External DNS recovery files and Docker volume contents must be saved separately.")
		if audit.HasErrors() {
			report.Notes = append(report.Notes, "The archive was saved with application audit errors; review them before restoring it.")
		}
		return report, nil
	}
	var result runtimebackend.Result
	var err error
	switch operation {
	case "runtime-migrate":
		result, err = runtimebackend.Migrate(directory)
	case "runtime-init":
		result, err = runtimebackend.InitSQLite(directory)
	case "runtime-restore":
		result, err = runtimebackend.Restore(directory, input)
	case "runtime-rollback":
		result, err = runtimebackend.Rollback(directory)
	case "runtime-verify":
		result, err = runtimebackend.Verify(directory)
	default:
		return report, fmt.Errorf("unsupported runtime command")
	}
	if err != nil {
		return report, err
	}
	report.Runtime = &result
	return report, nil
}
