package model

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func node(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Content[0]
}

func TestMergeYAMLNodeKeepsOrderStyleAndUnknownKeys(t *testing.T) {
	cur := node(t, "b: 'quoted'   # c\nx-extra: keep\na: 1\ngone: 2\n")
	next := node(t, "a: 2\nb: quoted\nadded: 3\n")
	known := node(t, "a: 1\nb: quoted\ngone: 2\n") // the model knows a, b, gone
	merged := MergeYAMLNode(cur, next, known)
	out, err := yaml.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	want := "b: 'quoted' # c\nx-extra: keep\na: 2\nadded: 3\n"
	if string(out) != want {
		t.Fatalf("merged:\n%s\nwant:\n%s", out, want)
	}
}

func TestSpliceFallsBackToAFullFormat(t *testing.T) {
	src := "items:\r\n  - name: a\r\n    v: 1\r\n"
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	item := doc.Content[0].Content[1].Content[0]
	item.Content[3].Value = "2"
	out, spliced, err := SpliceYAMLItems([]byte(src), &doc, []*yaml.Node{item})
	if err != nil || spliced {
		t.Fatalf("spliced=%v err=%v", spliced, err)
	}
	if !strings.Contains(string(out), "v: 2") {
		t.Fatalf("out:\n%s", out)
	}
}
