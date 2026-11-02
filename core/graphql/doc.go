// Package graphql checks GraphQL operations and responses against a schema.
//
// Schema text is parsed and validated with github.com/vektah/gqlparser/v2
// (MIT). Verity does not implement a GraphQL parser. SDL files stay inside
// the project directory. Introspection uses the step URL, auth, and headers,
// and the result is cached for one run, once per URL and auth identity.
// The cache key is a hash and is not logged. Fetched SDL and
// introspection error text go through a secrets.Redactor before anything
// displays them. Auth headers are never included in errors.
package graphql
