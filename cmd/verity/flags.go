package main

import "flag"

// parsePermuted parses a flag set allowing flags and positional arguments to be
// freely interspersed (Go's flag package stops at the first positional). It
// returns the collected positional arguments in order.
//
// This lets users write the natural form:
//
//	verity import openapi spec.yaml --out project.yaml --json
//
// as well as the strict form (flags first). Parsing repeatedly consumes one
// positional at a time and re-parses the remainder.
func parsePermuted(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		positionals = append(positionals, rest[0])
		args = rest[1:]
	}
	return positionals, nil
}
