package template

import (
	"fmt"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"

	"github.com/praxiscloud/praxis/pkg/types"
)

// ExtractVariableSchema parses CUE source and returns the variable schema.
// It inspects the "variables" field to determine types, constraints, defaults,
// and enumerations for each variable.
func ExtractVariableSchema(source []byte) (types.VariableSchema, error) {
	ctx := cuecontext.New()
	val := ctx.CompileBytes(source)
	if val.Err() != nil {
		return nil, fmt.Errorf("parse CUE: %w", val.Err())
	}

	varsVal := val.LookupPath(cue.ParsePath("variables"))
	if !varsVal.Exists() {
		return nil, nil // template has no variables block
	}

	schema := make(types.VariableSchema)
	iter, err := varsVal.Fields()
	if err != nil {
		return nil, fmt.Errorf("iterate variables: %w", err)
	}

	for iter.Next() {
		name := iter.Selector().String()
		field := iter.Value()
		schema[name] = analyzeVariableField(field)
	}

	return schema, nil
}

// analyzeVariableField inspects a CUE value to determine its type, whether
// it's required, its default value, any regex constraint, and enum values.
func analyzeVariableField(v cue.Value) types.VariableField {
	f := types.VariableField{}

	// Determine type from incomplete kind.
	switch v.IncompleteKind() {
	case cue.StringKind:
		f.Type = "string"
	case cue.BoolKind:
		f.Type = "bool"
	case cue.IntKind:
		f.Type = "int"
	case cue.FloatKind, cue.NumberKind:
		f.Type = "float"
	default:
		f.Type = "string"
	}

	// Check for default value. If Default() returns a concrete value without
	// error, the field has a default and is therefore not required.
	if defVal, ok := v.Default(); ok {
		f.Required = false
		if concrete, err := marshalDefault(defVal); err == nil {
			f.Default = concrete
		}
	} else {
		f.Required = true
	}

	// Check for disjunction (enum values).
	// For CUE disjunctions like "dev" | "staging" | "prod", we extract the
	// individual string values.
	op, args := v.Expr()
	if op == cue.OrOp && len(args) > 0 {
		var enumVals []string
		allStrings := true
		for _, arg := range args {
			if s, err := arg.String(); err == nil {
				enumVals = append(enumVals, s)
			} else {
				allStrings = false
				break
			}
		}
		if allStrings && len(enumVals) > 0 {
			f.Enum = enumVals
		}
	}

	return f
}

// marshalDefault extracts the Go value from a concrete CUE default.
func marshalDefault(v cue.Value) (any, error) {
	switch v.IncompleteKind() {
	case cue.StringKind:
		return v.String()
	case cue.BoolKind:
		return v.Bool()
	case cue.IntKind:
		i, err := v.Int64()
		return i, err
	case cue.FloatKind, cue.NumberKind:
		return v.Float64()
	default:
		return nil, fmt.Errorf("unsupported kind %v", v.IncompleteKind())
	}
}
