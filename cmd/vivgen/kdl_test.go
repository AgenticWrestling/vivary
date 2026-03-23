package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/sblinch/kdl-go"
	"github.com/sblinch/kdl-go/document"
	"github.com/sblinch/kdl-go/relaxed"
)

var capNamePattern = regexp.MustCompile(`^[A-Z][A-Za-z]+_[A-Z][A-Za-z]+_[A-Z][A-Za-z]+$`)

func TestLoadCapabilities_ParsesRealCatalog(t *testing.T) {
	caps, err := loadCapabilities(filepath.Join(repoRoot(), "capabilities"))
	if err != nil {
		t.Fatalf("loadCapabilities: %v", err)
	}
	if len(caps) == 0 {
		t.Fatal("expected capabilities from capabilities.kdl")
	}
}

func TestLoadCapabilities_ReferencesKnownCategoriesAndEntities(t *testing.T) {
	categories := loadCategories(t)
	entities := loadEntities(t)
	caps, err := loadCapabilities(filepath.Join(repoRoot(), "capabilities"))
	if err != nil {
		t.Fatalf("loadCapabilities: %v", err)
	}

	for _, cap := range caps {
		if !capNamePattern.MatchString(cap.Name) {
			t.Fatalf("capability name %q does not match Namespace_Noun_Verb", cap.Name)
		}
		if _, ok := categories[cap.Category]; !ok {
			t.Fatalf("capability %q references unknown category %q", cap.Name, cap.Category)
		}
		if cap.Description == "" {
			t.Fatalf("capability %q missing description", cap.Name)
		}
		for _, ent := range append(append([]string{}, cap.Reads...), cap.Writes...) {
			if _, ok := entities[ent]; !ok {
				t.Fatalf("capability %q references unknown entity %q", cap.Name, ent)
			}
		}
		for _, field := range cap.Fields {
			if field.Name == "" || field.Type == "" {
				t.Fatalf("capability %q has incomplete field metadata: %#v", cap.Name, field)
			}
		}
	}
}

func TestEntitiesReferenceKnownComponentsAndValidScopeFields(t *testing.T) {
	components := loadComponents(t)
	entities := loadEntities(t)
	if len(entities) == 0 {
		t.Fatal("expected entities from entities.kdl")
	}

	for _, ent := range entities {
		if ent.Description == "" {
			t.Fatalf("entity %q missing description", ent.Name)
		}
		for _, component := range ent.Components {
			componentFields, ok := components[component]
			if !ok {
				t.Fatalf("entity %q references unknown component %q", ent.Name, component)
			}
			for _, ref := range ent.ScopeFieldRefs {
				if ref.Component == component && ref.Field != "" && !componentFields[ref.Field] {
					t.Fatalf("entity %q scope ref %q.%q does not exist", ent.Name, ref.Component, ref.Field)
				}
			}
		}
	}
}

func TestLoadCapabilities_SpecificExamples(t *testing.T) {
	caps, err := loadCapabilities(filepath.Join(repoRoot(), "capabilities"))
	if err != nil {
		t.Fatalf("loadCapabilities: %v", err)
	}

	browserRead := mustFindCapability(t, caps, "Browser_Page_Read")
	if browserRead.Category != "Browser" {
		t.Fatalf("Browser_Page_Read category = %q, want Browser", browserRead.Category)
	}
	if len(browserRead.Fields) != 3 {
		t.Fatalf("Browser_Page_Read field count = %d, want 3", len(browserRead.Fields))
	}
	if browserRead.Fields[0].Name != "url" || !browserRead.Fields[0].Required {
		t.Fatalf("unexpected first field: %#v", browserRead.Fields[0])
	}

	fsWrite := mustFindCapability(t, caps, "Filesystem_File_Write")
	if fsWrite.Category != "Filesystem" {
		t.Fatalf("Filesystem_File_Write category = %q, want Filesystem", fsWrite.Category)
	}
	if fsWrite.Returns != "object" {
		t.Fatalf("Filesystem_File_Write returns = %q, want object", fsWrite.Returns)
	}
}

type entityInfo struct {
	Name           string
	Description    string
	Components     []string
	DeclaredFields map[string]bool
	ScopeFieldRefs []scopeFieldRef
}

type scopeFieldRef struct {
	Component string
	Field     string
}

func loadCategories(t *testing.T) map[string]bool {
	t.Helper()
	doc, err := parseKDLDoc(filepath.Join(repoRoot(), "capabilities", "categories.kdl"))
	if err != nil {
		t.Fatalf("parse categories.kdl: %v", err)
	}
	out := make(map[string]bool)
	for _, node := range doc.Nodes {
		if nodeName(node) == "category" {
			out[firstArg(node)] = true
		}
	}
	return out
}

func loadComponents(t *testing.T) map[string]map[string]bool {
	t.Helper()
	doc, err := parseKDLDoc(filepath.Join(repoRoot(), "capabilities", "components.kdl"))
	if err != nil {
		t.Fatalf("parse components.kdl: %v", err)
	}
	out := make(map[string]map[string]bool)
	for _, node := range doc.Nodes {
		if nodeName(node) != "component" {
			continue
		}
		fields := make(map[string]bool)
		for _, child := range node.Children {
			if nodeName(child) == "field" {
				fields[firstArg(child)] = true
			}
		}
		out[firstArg(node)] = fields
	}
	return out
}

func loadEntities(t *testing.T) map[string]entityInfo {
	t.Helper()
	doc, err := parseKDLDoc(filepath.Join(repoRoot(), "capabilities", "entities.kdl"))
	if err != nil {
		t.Fatalf("parse entities.kdl: %v", err)
	}
	out := make(map[string]entityInfo)
	for _, node := range doc.Nodes {
		if nodeName(node) != "entity" {
			continue
		}
		ent := entityInfo{
			Name:           firstArg(node),
			Description:    childValue(node, "description"),
			DeclaredFields: map[string]bool{},
		}
		for _, child := range node.Children {
			switch nodeName(child) {
			case "components":
				for _, arg := range child.Arguments {
					ent.Components = append(ent.Components, arg.ValueString())
				}
			case "fields":
				for _, fieldNode := range child.Children {
					if nodeName(fieldNode) == "field" {
						ent.DeclaredFields[firstArg(fieldNode)] = true
					}
				}
			case "scope-constraints":
				for _, constraint := range child.Children {
					ref := propValue(constraint, "field")
					parts := strings.Split(ref, ".")
					sf := scopeFieldRef{}
					if len(parts) == 1 {
						sf.Field = parts[0]
					} else if len(parts) == 2 {
						sf.Component = parts[0]
						sf.Field = parts[1]
					}
					ent.ScopeFieldRefs = append(ent.ScopeFieldRefs, sf)
				}
			}
		}
		out[ent.Name] = ent
	}
	return out
}

func parseKDLDoc(path string) (*document.Document, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return kdl.ParseWithOptions(f, kdl.ParseOptions{RelaxedNonCompliant: relaxed.NGINXSyntax})
}

func mustFindCapability(t *testing.T, caps []kdlCapability, name string) kdlCapability {
	t.Helper()
	for _, cap := range caps {
		if cap.Name == name {
			return cap
		}
	}
	t.Fatalf("capability %q not found", name)
	return kdlCapability{}
}

func repoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
