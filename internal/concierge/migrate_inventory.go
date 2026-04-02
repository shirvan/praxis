package concierge

import (
	"regexp"
	"strings"
)

// MigrationInventory summarizes the source files for the LLM.
type MigrationInventory struct {
	Format         string            `json:"format"`
	Files          []FileEntry       `json:"files"`
	ResourceTypes  []string          `json:"resourceTypes"`
	MappedKinds    map[string]string `json:"mappedKinds"`
	UnmappedTypes  []string          `json:"unmappedTypes"`
	TotalResources int               `json:"totalResources"`
}

// FileEntry represents a file in the migration source.
type FileEntry struct {
	Path string `json:"path"`
	Size int    `json:"size"`
}

var (
	// Terraform: resource "aws_s3_bucket" "name" {
	tfResourceRe = regexp.MustCompile(`resource\s+"([^"]+)"\s+"[^"]+"`)

	// CloudFormation YAML: Type: AWS::S3::Bucket
	cfYAMLTypeRe = regexp.MustCompile(`(?m)^\s*Type:\s+(AWS::\S+)`)

	// CloudFormation JSON: "Type": "AWS::S3::Bucket"
	cfJSONTypeRe = regexp.MustCompile(`"Type"\s*:\s*"(AWS::\S+)"`)

	// Crossplane: kind: Bucket
	cpKindRe = regexp.MustCompile(`(?m)^kind:\s+(\S+)`)
)

// BuildInventory scans source content and produces a migration inventory.
func BuildInventory(format, source string) MigrationInventory {
	inv := MigrationInventory{
		Format:      format,
		MappedKinds: make(map[string]string),
	}

	var matches [][]string
	switch format {
	case "terraform":
		matches = tfResourceRe.FindAllStringSubmatch(source, -1)
	case "cloudformation":
		if strings.Contains(source, `"Type"`) {
			matches = cfJSONTypeRe.FindAllStringSubmatch(source, -1)
		} else {
			matches = cfYAMLTypeRe.FindAllStringSubmatch(source, -1)
		}
	case "crossplane":
		matches = cpKindRe.FindAllStringSubmatch(source, -1)
	}

	seen := make(map[string]bool)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		resType := m[1]
		if seen[resType] {
			inv.TotalResources++
			continue
		}
		seen[resType] = true
		inv.TotalResources++
		inv.ResourceTypes = append(inv.ResourceTypes, resType)

		if kind, ok := LookupPraxisKind(resType); ok {
			inv.MappedKinds[resType] = kind
		} else {
			inv.UnmappedTypes = append(inv.UnmappedTypes, resType)
		}
	}

	return inv
}

// DetectFormat guesses the IaC format from content.
func DetectFormat(source string) string {
	if tfResourceRe.MatchString(source) {
		return "terraform"
	}
	if cfYAMLTypeRe.MatchString(source) || cfJSONTypeRe.MatchString(source) {
		return "cloudformation"
	}
	if cpKindRe.MatchString(source) && strings.Contains(source, "apiVersion:") {
		return "crossplane"
	}
	return "unknown"
}
