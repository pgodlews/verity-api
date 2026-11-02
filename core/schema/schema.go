// Package schema publishes Verity's versioned JSON Schemas for the project
// format and for auth provider configuration. Editors and AI agents use these
// to author and validate artifacts without guessing.
//
// The schemas are kept as Go raw-string constants so they are always available
// to the CLI (verity schema) and GUI without filesystem lookups. The
// standalone *.json files in this directory are generated from these constants
// (see gen/main.go) for editor/$schema consumption.
package schema

// ProjectSchemaJSON is the JSON Schema (2020-12) for a Verity project.
func ProjectSchemaJSON() string { return projectSchema }

// AuthSchemaJSON is the JSON Schema for the built-in auth provider configs.
func AuthSchemaJSON() string { return authSchema }

const projectSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://verity-api.dev/schemas/project-1.0.json",
  "title": "Verity Project",
  "description": "A Verity API contract testing project. Protocol-neutral, git-friendly.",
  "type": "object",
  "required": ["schemaVersion", "name", "suites"],
  "additionalProperties": false,
  "properties": {
    "schemaVersion": { "type": "string", "examples": ["1.0"] },
    "name": { "type": "string" },
    "description": { "type": "string" },
    "schema": { "$ref": "#/$defs/graphqlSchema" },
    "include": { "type": "array", "items": { "type": "string" } },
    "environments": { "type": "array", "items": { "$ref": "#/$defs/environment" } },
    "auth": { "type": "array", "items": { "$ref": "#/$defs/authDef" } },
    "suites": { "type": "array", "minItems": 1, "items": { "$ref": "#/$defs/suite" } },
    "mocks": {
      "type": "array",
      "description": "Optional responses served by verity mock. Not executed by verity run.",
      "items": { "$ref": "#/$defs/mock" }
    }
  },
  "$defs": {
    "graphqlSchema": {
      "type": "object",
      "additionalProperties": false,
      "description": "GraphQL schema source. Set file or introspect. A step value replaces the suite value, which replaces the project value. Used by graphql steps.",
      "properties": {
        "file": { "type": "string", "description": "SDL path relative to the project directory. The file must stay inside that directory." },
        "introspect": { "type": "boolean", "description": "When true, fetch the schema from the step URL with the step auth and headers. Cached per run, keyed by URL and auth identity." }
      }
    },
    "environment": {
      "type": "object",
      "required": ["name"],
      "additionalProperties": false,
      "properties": {
        "name": { "type": "string" },
        "default": { "type": "boolean" },
        "variables": { "type": "object", "additionalProperties": { "type": "string" } },
        "auth": { "$ref": "#/$defs/authRef" }
      }
    },
    "authDef": {
      "type": "object",
      "required": ["name", "type"],
      "additionalProperties": false,
      "properties": {
        "name": { "type": "string" },
        "type": { "type": "string", "examples": ["none", "basic", "digest", "bearer", "apikey", "mtls", "oauth2", "oauth2_client_credentials", "aws_sigv4", "hmac"] },
        "config": { "type": "object", "additionalProperties": true }
      }
    },
    "authRef": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "use": { "type": "array", "items": { "type": "string" } },
        "inherit": { "type": "boolean" }
      }
    },
    "suite": {
      "type": "object",
      "required": ["name", "cases"],
      "additionalProperties": false,
      "properties": {
        "name": { "type": "string" },
        "vars": { "type": "object", "additionalProperties": { "type": "string" } },
        "auth": { "$ref": "#/$defs/authRef" },
        "schema": { "$ref": "#/$defs/graphqlSchema" },
        "parallel": { "type": "integer", "minimum": 0 },
        "setup": { "$ref": "#/$defs/hook" },
        "teardown": { "$ref": "#/$defs/hook" },
        "cases": { "type": "array", "minItems": 1, "items": { "$ref": "#/$defs/case" } }
      }
    },
    "hook": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "name": { "type": "string" },
        "steps": { "type": "array", "items": { "$ref": "#/$defs/step" } }
      }
    },
    "case": {
      "type": "object",
      "required": ["name", "steps"],
      "additionalProperties": false,
      "properties": {
        "name": { "type": "string" },
        "vars": { "type": "object", "additionalProperties": { "type": "string" } },
        "auth": { "$ref": "#/$defs/authRef" },
        "continueOnError": { "type": "boolean" },
        "feed": { "$ref": "#/$defs/feed" },
        "steps": { "type": "array", "minItems": 1, "items": { "$ref": "#/$defs/step" } }
      }
    },
    "feed": {
      "type": "object",
      "required": ["type"],
      "additionalProperties": false,
      "properties": {
        "type": { "type": "string", "examples": ["csv", "json"] },
        "path": { "type": "string" },
        "inline": { "type": "array", "items": { "type": "object", "additionalProperties": { "type": "string" } } },
        "config": { "type": "object", "additionalProperties": true }
      }
    },
    "step": {
      "type": "object",
      "required": ["name", "protocol"],
      "additionalProperties": false,
      "properties": {
        "name": { "type": "string" },
        "protocol": { "type": "string", "examples": ["rest", "soap", "graphql", "grpc"] },
        "schema": { "$ref": "#/$defs/graphqlSchema" },
        "request": { "type": "object", "additionalProperties": true, "description": "Protocol-specific. REST: method, url, headers, query, and one of json, form, body, multipart (ordered parts with name plus value, or name plus file, contentType, and filename), or bodyFile. Relative upload paths resolve against the project directory when the request is sent. GraphQL: url, query, variables, operationName, headers." },
        "auth": { "$ref": "#/$defs/authRef" },
        "vars": { "type": "object", "additionalProperties": { "type": "string" } },
        "skip": { "type": "boolean" },
        "timeoutMs": { "type": "integer", "minimum": 0, "description": "Per-attempt HTTP timeout in milliseconds. Overrides the run timeout. Each attempt gets its own budget. 0 uses the run timeout." },
        "retry": {
          "type": "object",
          "additionalProperties": false,
          "required": ["count", "until"],
          "description": "Re-execute the request. Total attempts are 1 + count.",
          "properties": {
            "count": { "type": "integer", "minimum": 0, "maximum": 20, "description": "Extra attempts after the first." },
            "delayMs": { "type": "integer", "minimum": 0, "description": "Pause between attempts, in milliseconds. Cancelled with the run." },
            "until": { "type": "string", "enum": ["passed", "status"], "description": "passed: retry on assertion failure or transport error. status: retry only on transport error, HTTP 429, or HTTP 5xx." }
          }
        },
        "assertions": { "type": "array", "items": { "$ref": "#/$defs/assertion" } },
        "transfers": { "type": "array", "items": { "$ref": "#/$defs/transfer" } }
      }
    },
    "assertion": {
      "type": "object",
      "required": ["type"],
      "additionalProperties": false,
      "properties": {
        "type": { "type": "string", "examples": ["status", "response_time", "jsonpath", "xpath", "json_schema", "xsd", "contains", "regex", "header", "graphql_schema"] },
        "description": { "type": "string" },
        "expr": { "type": "string" },
        "op": { "type": "string", "enum": ["eq", "ne", "lt", "lte", "gt", "gte", "contains", "matches", "exists", "not_exists"] },
        "value": {},
        "config": { "type": "object", "additionalProperties": true }
      }
    },
    "transfer": {
      "type": "object",
      "required": ["name", "from"],
      "additionalProperties": false,
      "properties": {
        "name": { "type": "string" },
        "from": { "type": "string", "enum": ["jsonpath", "xpath", "header", "status", "body", "regex"] },
        "expr": { "type": "string" },
        "default": { "type": "string" },
        "scope": { "type": "string", "enum": ["case", "run"] }
      }
    },
    "mock": {
      "type": "object",
      "required": ["method", "path"],
      "additionalProperties": false,
      "not": { "required": ["body", "json"] },
      "description": "One mock response. Set body or json, not both. Omitted status means 200.",
      "properties": {
        "method": { "type": "string", "description": "HTTP method. GET, POST, PUT, PATCH, DELETE, HEAD, or OPTIONS." },
        "path": { "type": "string", "description": "Path template. {name} matches one path segment." },
        "status": { "type": "integer", "minimum": 100, "maximum": 599 },
        "headers": { "type": "object", "additionalProperties": { "type": "string" } },
        "body": { "type": "string" },
        "json": { "description": "JSON response body. Mutually exclusive with body." },
        "delayMs": { "type": "integer", "minimum": 0, "description": "Per-response delay. Overrides verity mock --delay when greater than zero." },
        "match": { "$ref": "#/$defs/mockMatch" }
      }
    },
    "mockMatch": {
      "type": "object",
      "additionalProperties": false,
      "description": "Every set field must match. Header names are case-insensitive; values are exact.",
      "properties": {
        "query": { "type": "object", "additionalProperties": { "type": "string" } },
        "headers": { "type": "object", "additionalProperties": { "type": "string" } },
        "bodyContains": { "type": "string" }
      }
    }
  }
}`

const authSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://verity-api.dev/schemas/auth-1.0.json",
  "title": "Verity Auth Provider Config",
  "description": "Configuration shapes for built-in auth providers. Secrets must use ${env:VAR} or ${secret:...} references, never literals.",
  "oneOf": [
    {
      "title": "none",
      "type": "object",
      "properties": { "type": { "const": "none" }, "config": { "type": "object" } },
      "required": ["type"]
    },
    {
      "title": "basic",
      "type": "object",
      "properties": {
        "type": { "const": "basic" },
        "config": {
          "type": "object",
          "required": ["username", "password"],
          "properties": {
            "username": { "type": "string" },
            "password": { "type": "string", "description": "Use ${env:VAR}." }
          }
        }
      },
      "required": ["type", "config"]
    },
    {
      "title": "digest",
      "type": "object",
      "properties": {
        "type": { "const": "digest" },
        "config": {
          "type": "object",
          "required": ["username", "password"],
          "properties": {
            "username": { "type": "string", "minLength": 1 },
            "password": { "type": "string", "minLength": 1, "description": "Use ${env:VAR}." },
            "algorithm": { "enum": ["MD5", "MD5-sess", "SHA-256", "SHA-256-sess"] }
          }
        }
      },
      "required": ["type", "config"]
    },
    {
      "title": "bearer",
      "type": "object",
      "properties": {
        "type": { "const": "bearer" },
        "config": {
          "type": "object",
          "required": ["token"],
          "properties": { "token": { "type": "string", "description": "Use ${env:VAR}." } }
        }
      },
      "required": ["type", "config"]
    },
    {
      "title": "apikey",
      "type": "object",
      "properties": {
        "type": { "const": "apikey" },
        "config": {
          "type": "object",
          "required": ["name", "value"],
          "properties": {
            "name": { "type": "string" },
            "value": { "type": "string", "description": "Use ${env:VAR}." },
            "in": { "type": "string", "enum": ["header", "query", "cookie"], "default": "header" }
          }
        }
      },
      "required": ["type", "config"]
    },
    {
      "title": "mtls",
      "type": "object",
      "properties": {
        "type": { "const": "mtls" },
        "config": {
          "type": "object",
          "properties": {
            "certPEM": { "type": "string" },
            "keyPEM": { "type": "string" },
            "caPEM": { "type": "string" },
            "certFile": { "type": "string" },
            "keyFile": { "type": "string" },
            "caFile": { "type": "string" },
            "minVersion": { "type": "string", "enum": ["1.0", "1.1", "1.2", "1.3"] },
            "maxVersion": { "type": "string", "enum": ["1.0", "1.1", "1.2", "1.3"] },
            "insecureSkipVerify": { "type": "boolean" }
          }
        }
      },
      "required": ["type", "config"]
    },
    {
      "title": "oauth2 (oauth2_client_credentials alias)",
      "type": "object",
      "properties": {
        "type": { "enum": ["oauth2", "oauth2_client_credentials"] },
        "config": {
          "type": "object",
          "required": ["tokenUrl", "clientId"],
          "properties": {
            "grant": { "type": "string", "enum": ["client_credentials", "password", "refresh_token", "authorization_code"], "default": "client_credentials" },
            "authUrl": { "type": "string", "minLength": 1 },
            "redirectPort": { "type": "integer", "minimum": 0, "maximum": 65535, "default": 0 },
            "scopes": { "oneOf": [{ "type": "string" }, { "type": "array", "items": { "type": "string" } }] },
            "tokenUrl": { "type": "string", "minLength": 1 },
            "clientId": { "type": "string", "minLength": 1 },
            "clientSecret": { "type": "string", "minLength": 1, "description": "Use ${env:VAR}." },
            "username": { "type": "string", "minLength": 1 },
            "password": { "type": "string", "minLength": 1, "description": "Use ${env:VAR}." },
            "refreshToken": { "type": "string", "minLength": 1, "description": "Use ${env:VAR}; rotation is kept in memory only." },
            "scope": { "type": "string" },
            "authStyle": { "type": "string", "enum": ["header", "body"], "default": "header" },
            "refreshSkewSeconds": { "type": "integer", "minimum": 0 }
          },
          "allOf": [
            {
              "if": { "properties": { "grant": { "const": "authorization_code" } }, "required": ["grant"] },
              "then": { "required": ["authUrl"] },
              "else": { "required": ["clientSecret"] }
            },
            {
              "if": { "properties": { "grant": { "const": "password" } }, "required": ["grant"] },
              "then": { "required": ["username", "password"] }
            },
            {
              "if": { "properties": { "grant": { "const": "refresh_token" } }, "required": ["grant"] },
              "then": { "required": ["refreshToken"] }
            }
          ]
        }
      },
      "required": ["type", "config"]
    },
    {
      "title": "aws_sigv4",
      "type": "object",
      "properties": {
        "type": { "const": "aws_sigv4" },
        "config": {
          "type": "object",
          "required": ["region", "service"],
          "additionalProperties": false,
          "properties": {
            "accessKeyId": { "type": "string", "description": "Omit to read AWS_ACCESS_KEY_ID. Use ${env:VAR} when set." },
            "secretAccessKey": { "type": "string", "description": "Omit to read AWS_SECRET_ACCESS_KEY. Use ${env:VAR} when set." },
            "sessionToken": { "type": "string", "description": "Omit to read AWS_SESSION_TOKEN when that variable is set. Use ${env:VAR} when set." },
            "region": { "type": "string", "minLength": 1 },
            "service": { "type": "string", "minLength": 1, "description": "Signing name such as s3 or execute-api. s3 uses single URI encoding and does not normalize the path." },
            "unsignedPayload": { "type": "boolean", "default": false, "description": "When true, sign UNSIGNED-PAYLOAD instead of the body hash." }
          }
        }
      },
      "required": ["type", "config"]
    },
    {
      "title": "hmac",
      "type": "object",
      "properties": {
        "type": { "const": "hmac" },
        "config": {
          "type": "object",
          "required": ["secret"],
          "additionalProperties": false,
          "properties": {
            "secret": { "type": "string", "minLength": 1, "description": "Use ${env:VAR}." },
            "algorithm": { "type": "string", "enum": ["sha1", "sha256", "sha512", "SHA1", "SHA256", "SHA512"], "default": "sha256" },
            "header": { "type": "string", "pattern": "^[!#$%&'*+.^_\u0060|~0-9A-Za-z-]+$", "default": "X-Signature" },
            "encoding": { "type": "string", "enum": ["hex", "base64", "HEX", "BASE64"], "default": "hex" },
            "prefix": { "type": "string", "description": "Text prepended to the encoded MAC, such as sha256=." },
            "message": { "type": "string", "description": "Template. ${method}, ${path}, ${query}, ${body}, ${timestamp}, and ${nonce} are filled when the request is signed. Omit to sign the raw body." },
            "timestampHeader": { "type": "string", "pattern": "^[!#$%&'*+.^_\u0060|~0-9A-Za-z-]+$" },
            "nonceHeader": { "type": "string", "pattern": "^[!#$%&'*+.^_\u0060|~0-9A-Za-z-]+$" }
          }
        }
      },
      "required": ["type", "config"]
    }
  ]
}`
