package main

// Exit codes, designed for CI/CD. These are stable and documented in the CLI
// usage text and docs/ci.md.
const (
	exitOK          = 0 // success: all tests passed
	exitTestFailed  = 1 // assertion or transport failure occurred
	exitUsage       = 2 // invalid command-line usage
	exitConfig      = 3 // validation/config/load error
	exitInternal    = 4 // unexpected internal error
	exitInterrupted = 5 // run cancelled (SIGINT/SIGTERM); partial reports were written
)
