# Desktop regression results

Copy this file for each run and fill it in. See
[the regression suite](regression-suite.md) for the scenarios and rules. Do not
include usernames, home paths, hostnames, or tokens.

## Run

| Field | Value |
| --- | --- |
| Commit under test | |
| Change being checked (branch or PR) | |
| OS and version | |
| Architecture | |
| Linux session (X11 or Wayland) | |
| How the app was started (built binary or `wails dev`) | |
| Tester (agent name and model, or role) | |
| Date | |
| Scenarios selected (smoke only, or smoke plus which extended) | |

## Results

Status is one of **pass**, **fail**, **blocked**, or **not run**.

| ID | Scenario | Status | Notes |
| --- | --- | --- | --- |
| RG-01 | Launch and open a project | | |
| RG-02 | Send a REST request | | |
| RG-03 | Send a SOAP request | | |
| RG-04 | Run collection, check redaction | | |
| RG-05 | Edit, save, verify file on disk | | |
| RG-06 | Source control change and commit | | |
| RG-07 | Plaintext credentials cannot be saved | | |
| RG-08 | Environment variable persists | | |
| RG-09 | Load test run, bad input, report kept | | |
| RG-10 | Import OpenAPI | | |
| RG-11 | Failing request is reported | | |
| RG-12 | Missing credentials fail only secured request | | |
| RG-13 | External changes and draft protection | | |
| RG-14 | Cancel a running load test | | |
| RG-15 | Branches | | |
| RG-16 | Import WSDL, Postman, SoapUI | | |
| RG-17 | Theme and search | | |

## Failures and blocked scenarios

Repeat this block for each one.

- **ID and step:**
- **Expected:**
- **Observed:**
- **Evidence (sanitized):**
- **Retried:** yes / no

## Labels that differed from the suite

List any control whose text or accessible name no longer matches the suite, so
the suite can be updated.

## Verdict

**Pass / Fail / Incomplete** — one sentence saying why.
