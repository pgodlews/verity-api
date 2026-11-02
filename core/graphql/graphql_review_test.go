package graphql

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/auth"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

const petSDL = `
union Pet = Dog | Cat
type Dog { name: String! bark: Boolean }
type Cat { name: String! meow: Boolean }
interface Node { id: ID! }
interface Named implements Node { id: ID! name: String }
type User implements Named & Node { id: ID! name: String }
interface Speaker { name: String! }
type Pup implements Speaker { name: String! bark: Boolean }
type Kitten implements Speaker { name: String! meow: Boolean }
type Query {
  pet: Pet
  user: User
  speaker: Speaker
  score: Int
  ident: ID
  ratio: Float
}
`

func TestAbstractResponseNeedsTypename(t *testing.T) {
	schema, err := ParseSDL(petSDL, "pet.graphql")
	if err != nil {
		t.Fatal(err)
	}
	pet := "{ pet { ... on Dog { name bark } ... on Cat { name meow } } }"
	speaker := "{ speaker { name ... on Pup { bark } ... on Kitten { meow } } }"
	cases := []struct {
		name  string
		query string
		body  string
		fail  string
		pass  bool
	}{
		{"empty union", pet, `{"data":{"pet":{}}}`, "__typename needed to check an abstract type", false},
		{"both members", pet, `{"data":{"pet":{"name":"a"}}}`, "__typename needed to check an abstract type", false},
		{"fields from both", pet, `{"data":{"pet":{"name":"a","bark":true,"meow":true}}}`, "__typename needed to check an abstract type", false},
		{"one member", pet, `{"data":{"pet":{"name":"a","bark":true}}}`, "", true},
		{"typename", pet, `{"data":{"pet":{"__typename":"Dog","name":"a","bark":true}}}`, "", true},
		{"interface fields still checked", speaker, `{"data":{"speaker":{}}}`, "missing field", false},
		{"interface unique member", speaker, `{"data":{"speaker":{"name":"a","bark":true}}}`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := CheckResponse(schema, tc.query, "", []byte(tc.body), false)
			if tc.pass {
				if len(problems) != 0 {
					t.Fatal(problems)
				}
				return
			}
			if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), tc.fail) {
				t.Fatalf("got %v", problems)
			}
		})
	}
	emptySpeaker := CheckResponse(schema, speaker, "", []byte(`{"data":{"speaker":{}}}`), false)
	if !strings.Contains(strings.Join(emptySpeaker, "\n"), "__typename needed") {
		t.Fatalf("interface member fragments: %v", emptySpeaker)
	}
}

func TestGrandparentInterfaceFragment(t *testing.T) {
	schema, err := ParseSDL(petSDL, "pet.graphql")
	if err != nil {
		t.Fatal(err)
	}
	// gqlparser requires the grandparent on the implements clause, then stores
	// only that clause. Drop Node so the check has to walk Named.
	user := schema.Types["User"]
	var direct []string
	for _, iface := range user.Interfaces {
		if iface != "Node" {
			direct = append(direct, iface)
		}
	}
	if len(direct) == 0 {
		t.Fatal("User has no direct interface")
	}
	user.Interfaces = direct
	body := []byte(`{"data":{"user":{"__typename":"User","id":"1"}}}`)
	inline := "{ user { ... on Node { id } } }"
	if p := CheckResponse(schema, inline, "", body, false); len(p) != 0 {
		t.Fatal(p)
	}
	named := "query { user { ...N } } fragment N on Node { id }"
	if p := CheckResponse(schema, named, "", body, false); len(p) != 0 {
		t.Fatal(p)
	}
}

func TestScalarRanges(t *testing.T) {
	schema, err := ParseSDL(petSDL, "pet.graphql")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		query string
		body  string
		pass  bool
	}{
		{"int32 max", "{ score }", `{"data":{"score":2147483647}}`, true},
		{"int32 min", "{ score }", `{"data":{"score":-2147483648}}`, true},
		{"int32 overflow", "{ score }", `{"data":{"score":2147483648}}`, false},
		{"int past 2^53", "{ score }", `{"data":{"score":9007199254740993}}`, false},
		{"int fraction", "{ score }", `{"data":{"score":1.5}}`, false},
		{"id big integer", "{ ident }", `{"data":{"ident":9007199254740993}}`, true},
		{"id string", "{ ident }", `{"data":{"ident":"abc"}}`, true},
		{"id fraction", "{ ident }", `{"data":{"ident":1.5}}`, false},
		{"float past 2^53", "{ ratio }", `{"data":{"ratio":9007199254740993}}`, true},
		{"float overflow", "{ ratio }", `{"data":{"ratio":1e309}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := CheckResponse(schema, tc.query, "", []byte(tc.body), false)
			if tc.pass && len(problems) != 0 {
				t.Fatal(problems)
			}
			if !tc.pass && len(problems) == 0 {
				t.Fatal("expected a type error")
			}
		})
	}
}

func TestRequiredVariablesAndInterpolation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "schema.graphql"), []byte(sampleSDL), 0o644); err != nil {
		t.Fatal(err)
	}
	query := "query ($id: ID!) {\n  user(id: $id) {\n    name\n  }\n}\n"
	missing := projectWith(dir, &model.GraphQLSchema{File: "schema.graphql"}, query, map[string]any{})
	errs := ValidateProject(context.Background(), missing, Options{})
	if len(errs) == 0 || errs[0].Path != "suites[0].cases[0].steps[0].request.variables" || !strings.Contains(errs[0].Error(), `required variable "id" is missing`) {
		t.Fatalf("missing id: %v", errs)
	}
	omitted := projectWith(dir, &model.GraphQLSchema{File: "schema.graphql"}, query, nil)
	errs = ValidateProject(context.Background(), omitted, Options{})
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), `required variable "id" is missing`) {
		t.Fatalf("omitted variables: %v", errs)
	}
	placeholder := projectWith(dir, &model.GraphQLSchema{File: "schema.graphql"}, query, map[string]any{"id": "${env:ID}"})
	if errs = ValidateProject(context.Background(), placeholder, Options{}); len(errs) != 0 {
		t.Fatalf("placeholder should be skipped: %v", errs)
	}

	p := projectWith(dir, &model.GraphQLSchema{File: "schema.graphql"}, "query {\n  user(id: \"1\") {\n    ${FIELD}\n  }\n}\n", nil)
	p.Environments = []model.Environment{{Name: "staging", Default: true, Variables: map[string]string{"FIELD": "name"}}}
	if errs = ValidateProject(context.Background(), p, Options{Environment: "staging"}); len(errs) != 0 {
		t.Fatalf("interpolated field: %v", errs)
	}
	p.Suites[0].Cases[0].Steps[0].Request["query"] = "query GetUser {\n  user(id: \"1\") {\n    name\n  }\n}\nquery Other {\n  ping\n}\n"
	p.Suites[0].Cases[0].Steps[0].Request["operationName"] = "${OP}"
	if errs = ValidateProject(context.Background(), p, Options{}); len(errs) != 0 {
		t.Fatalf("unresolved operation name should be unchecked: %v", errs)
	}
	p.Environments[0].Variables["OP"] = "GetUser"
	if errs = ValidateProject(context.Background(), p, Options{}); len(errs) != 0 {
		t.Fatalf("resolved operation name: %v", errs)
	}
	if errs = ValidateProject(context.Background(), p, Options{Environment: "nope"}); len(errs) == 0 || !strings.Contains(errs[0].Error(), "nope") {
		t.Fatalf("unknown environment: %v", errs)
	}
	raw := projectWith(dir, &model.GraphQLSchema{File: "schema.graphql"}, "query {\n  user(id: \"1\") {\n    nope\n  }\n}\n", nil)
	errs = ValidateProject(context.Background(), raw, Options{})
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "line 3, column 5 of the query") {
		t.Fatalf("query position: %v", errs)
	}
}

func TestIntrospectionIdentityAndReplay(t *testing.T) {
	var calls atomic.Int32
	var slowEntered atomic.Int32
	release := make(chan struct{})
	stop, cancel := context.WithCancel(context.Background())
	defer cancel()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") == "Bearer slow" && slowEntered.Add(1) == 1 {
			select {
			case <-release:
			case <-stop.Done():
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(introBody())
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(introBody())
	}))
	defer fast.Close()

	cache := NewCache()
	ctx := context.Background()
	client := slow.Client()
	slowReq := &spi.Request{URL: slow.URL, Headers: http.Header{"Authorization": {"Bearer slow"}}}
	var wg sync.WaitGroup
	wg.Add(2)
	errCh := make(chan error, 2)
	go func() {
		defer wg.Done()
		_, _, err := cache.LoadIntrospection(ctx, client, slowReq, nil, []string{"slow"}, nil)
		errCh <- err
	}()
	go func() {
		defer wg.Done()
		_, _, err := cache.LoadIntrospection(ctx, client, slowReq, nil, []string{"slow"}, nil)
		errCh <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for slowEntered.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("singleflight calls = %d", calls.Load())
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := cache.LoadIntrospection(ctx, fast.Client(), &spi.Request{URL: fast.URL}, nil, nil, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a different URL waited on the in-flight fetch")
	}
	close(release)
	wg.Wait()
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}

	before := calls.Load()
	other := &spi.Request{URL: slow.URL, Headers: http.Header{"Authorization": {"Bearer other"}}}
	if _, _, err := cache.LoadIntrospection(ctx, client, other, nil, []string{"slow"}, nil); err != nil {
		t.Fatal(err)
	}
	named := &spi.Request{URL: slow.URL, Headers: http.Header{"Authorization": {"Bearer other"}}}
	if _, _, err := cache.LoadIntrospection(ctx, client, named, nil, []string{"other"}, nil); err != nil {
		t.Fatal(err)
	}
	cooked := &spi.Request{URL: slow.URL, Headers: http.Header{"Cookie": {"sid=a"}}}
	if _, _, err := cache.LoadIntrospection(ctx, client, cooked, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	cert := &spi.Request{URL: slow.URL, TLS: &spi.TLSConfig{ClientCertPEM: []byte("cert-a")}}
	if _, _, err := cache.LoadIntrospection(ctx, client, cert, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before+4 {
		t.Fatalf("auth identity calls = %d, want %d", calls.Load(), before+4)
	}
	if _, _, err := cache.LoadIntrospection(ctx, client, cert, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before+4 {
		t.Fatal("same cert fingerprint fetched again")
	}
}

type pathChallenge struct {
	path   string
	method string
}

func (p *pathChallenge) RespondChallenge(_ context.Context, req *http.Request, resp *http.Response) (bool, error) {
	if resp.StatusCode != http.StatusUnauthorized {
		return false, nil
	}
	p.path = req.URL.Path
	p.method = req.Method
	req.Header.Set("Authorization", "Digest test")
	return true, nil
}

func TestDigestReplayUsesRedirectedRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			http.Redirect(w, r, "/graphql/", http.StatusFound)
			return
		}
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Digest realm="t", nonce="n", qop="auth"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(introBody())
	}))
	defer srv.Close()
	challenge := &pathChallenge{}
	_, _, err := introspect(context.Background(), srv.Client(), srv.URL+"/graphql", &spi.Request{URL: srv.URL + "/graphql"}, nil, []spi.ChallengeResponder{challenge}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if challenge.path != "/graphql/" {
		t.Fatalf("challenge path = %s", challenge.path)
	}
}

func TestOAuthSessionIntrospection(t *testing.T) {
	token := "session-token"
	var saw atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+token {
			saw.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(introBody())
	}))
	defer srv.Close()
	session, err := auth.NewOAuthSession(map[string]any{
		"grant": "authorization_code", "authUrl": srv.URL + "/auth", "tokenUrl": srv.URL + "/token", "clientId": "client",
	}, &auth.OAuthTokens{AccessToken: token, TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour)}, secrets.NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	p := projectWith("", &model.GraphQLSchema{Introspect: true}, "query {\n  user(id: \"1\") {\n    id\n  }\n}\n", nil)
	p.Auth = []model.AuthDef{{
		Name: "login", Type: "oauth2",
		Config: map[string]any{"grant": "authorization_code", "authUrl": srv.URL + "/auth", "tokenUrl": srv.URL + "/token", "clientId": "client"},
	}}
	p.Suites[0].Cases[0].Steps[0].Auth = &model.AuthRef{Use: []string{"login"}}
	p.Suites[0].Cases[0].Steps[0].Request["url"] = srv.URL
	opt := Options{Registry: registry.New(), Timeout: 5 * time.Second, AuthSessions: map[string]*auth.OAuthSession{"login": session}}
	if errs := ValidateProject(context.Background(), p, opt); len(errs) != 0 {
		t.Fatalf("session validate: %v", errs)
	}
	if !saw.Load() {
		t.Fatal("introspection did not send the desktop session")
	}
	saw.Store(false)
	opt.AuthSessions = nil
	errs := ValidateProject(context.Background(), p, opt)
	if len(errs) == 0 || saw.Load() {
		t.Fatalf("missing session should fail closed: %v", errs)
	}
}

func TestTimeoutHonouredWhenClientIsProvided(t *testing.T) {
	// Closing the client connection does not cancel this handler: the
	// server only notices a dropped connection on its next read or write.
	// done lets the test release the handler after the client has given up.
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	defer srv.Close()
	defer close(done)
	p := projectWith("", &model.GraphQLSchema{Introspect: true}, "{ ping }", nil)
	p.Suites[0].Cases[0].Steps[0].Request["url"] = srv.URL
	start := time.Now()
	errs := ValidateProject(context.Background(), p, Options{
		Timeout: 200 * time.Millisecond,
		NewClient: func(*spi.TLSConfig) (*http.Client, error) {
			return &http.Client{Timeout: time.Hour}, nil
		},
	})
	if len(errs) == 0 {
		t.Fatal("expected the client timeout")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("Options.Timeout was ignored, took %s", time.Since(start))
	}
}

func TestMultilineDefaultValue(t *testing.T) {
	str := "String"
	def := "\"a\nb\""
	env := introEnvelope{}
	env.Data.Schema = &introSchema{
		QueryType: &introName{Name: "Query"},
		Types: []introFullType{{
			Kind: "OBJECT", Name: "Query",
			Fields: []introField{{
				Name: "ping",
				Args: []introValue{{
					Name:         "q",
					Type:         introTypeRef{Kind: "NON_NULL", OfType: &introTypeRef{Kind: "SCALAR", Name: &str}},
					DefaultValue: &def,
				}},
				Type: introTypeRef{Kind: "SCALAR", Name: &str},
			}},
		}},
	}
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	sdl, schema, err := SDLFromIntrospection(body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sdl, `\n`) && !strings.Contains(sdl, "\n") {
		t.Fatalf("default dropped:\n%s", sdl)
	}
	if strings.Contains(sdl, "q: String!") && !strings.Contains(sdl, "=") {
		t.Fatalf("default changed nullability:\n%s", sdl)
	}
	if problems := CheckQuery(schema, "query { ping }", "", nil); len(problems) != 0 {
		t.Fatalf("omitted defaulted argument: %v\n%s", problems, sdl)
	}
}

func TestAbstractTemplates(t *testing.T) {
	schema, err := ParseSDL(`
union Pet = Dog | Cat
type Dog { name: String bark: Boolean owner: User }
type Cat { name: String meow: Boolean }
type User { id: ID }
interface Speaker { name: String }
type Pup implements Speaker { name: String bark: Boolean }
type Query { pet: Pet speaker: Speaker }
`, "abstract.graphql")
	if err != nil {
		t.Fatal(err)
	}
	pet, err := Generate(schema, "Query.pet", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := "query pet {\n  pet {\n    __typename\n    ... on Dog {\n      name\n      bark\n    }\n    ... on Cat {\n      name\n      meow\n    }\n  }\n}\n"
	if pet.Query != want {
		t.Fatalf("union:\n%s", pet.Query)
	}
	deep, err := Generate(schema, "Query.pet", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(deep.Query, "owner {") {
		t.Fatalf("depth 2 should enter the member object:\n%s", deep.Query)
	}
	speaker, err := Generate(schema, "Query.speaker", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(speaker.Query, "__typename") || !strings.Contains(speaker.Query, "name") || !strings.Contains(speaker.Query, "... on Pup {") || !strings.Contains(speaker.Query, "bark") {
		t.Fatalf("interface:\n%s", speaker.Query)
	}
	var members []string
	for i := 0; i < 11; i++ {
		members = append(members, "M"+string(rune('A'+i)))
	}
	var b strings.Builder
	b.WriteString("union Many = " + strings.Join(members, " | ") + "\n")
	for _, name := range members {
		b.WriteString("type " + name + " { id: ID }\n")
	}
	b.WriteString("type Query { many: Many }\n")
	manySchema, err := ParseSDL(b.String(), "many.graphql")
	if err != nil {
		t.Fatal(err)
	}
	many, err := Generate(manySchema, "Query.many", 1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(many.Query, "... on ") != 10 || !strings.Contains(many.Query, "# truncated") {
		t.Fatalf("truncation:\n%s", many.Query)
	}
}
