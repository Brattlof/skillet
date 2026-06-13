package registry

import "testing"

func TestLoadNotEmpty(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("registry is empty; expected at least the hello-skill example")
	}
}

func TestFind(t *testing.T) {
	e, ok, err := Find("hello-skill")
	if err != nil {
		t.Fatalf("Find() error: %v", err)
	}
	if !ok {
		t.Fatal("expected to find hello-skill")
	}
	if e.Repo == "" || e.Path == "" {
		t.Fatalf("hello-skill is missing repo/path: %+v", e)
	}

	if _, ok, _ := Find("does-not-exist"); ok {
		t.Fatal("did not expect to find a bogus skill")
	}
}

func TestSearch(t *testing.T) {
	res, err := Search("example")
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(res) == 0 {
		t.Fatal("expected a search hit for 'example'")
	}

	all, err := Search("")
	if err != nil {
		t.Fatalf("Search(\"\") error: %v", err)
	}
	if len(all) < len(res) {
		t.Fatal("empty query should return all entries")
	}
}
