package gcptarget

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"text/template"

	"github.com/forseti-security/config-validator/pkg/api/validator"
	asset2 "github.com/forseti-security/config-validator/pkg/asset"
	"github.com/gogo/protobuf/jsonpb"
	"github.com/open-policy-agent/frameworks/constraint/pkg/client"
	"github.com/open-policy-agent/frameworks/constraint/pkg/types"
	"github.com/pkg/errors"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Name is the target name for GCPTarget
const Name = "validation.gcp.forsetisecurity.org"

// GCPTarget is the constraint framework target for FCV
type GCPTarget struct {
}

var _ client.TargetHandler = &GCPTarget{}

// New returns a new GCPTarget
func New() *GCPTarget {
	return &GCPTarget{}
}

// MatchSchema implements client.MatchSchemaProvider
func (g *GCPTarget) MatchSchema() apiextensions.JSONSchemaProps {
	return apiextensions.JSONSchemaProps{
		Properties: map[string]apiextensions.JSONSchemaProps{
			"target": {
				Type: "array",
				Items: &apiextensions.JSONSchemaPropsOrArray{
					Schema: &apiextensions.JSONSchemaProps{
						Type: "string",
					},
				},
			},
			"exclude": {
				Type: "array",
				Items: &apiextensions.JSONSchemaPropsOrArray{
					Schema: &apiextensions.JSONSchemaProps{
						Type: "string",
					},
				},
			},
		},
	}
}

// GetName implements client.TargetHandler
func (g *GCPTarget) GetName() string {
	return Name
}

// Library implements client.TargetHandler
func (g *GCPTarget) Library() *template.Template {
	return libraryTemplate
}

// ProcessData implements client.TargetHandler
func (g *GCPTarget) ProcessData(obj interface{}) (bool, string, interface{}, error) {
	return false, "", nil, errors.Errorf("Storing data for referential constraint eval is not supported at this time.")
}

// HandleReview implements client.TargetHandler
func (g *GCPTarget) HandleReview(obj interface{}) (bool, interface{}, error) {
	switch asset := obj.(type) {
	case *validator.Asset:
		return g.handleAsset(asset)
	case map[string]interface{}:
		if _, found, err := unstructured.NestedString(asset, "name"); !found || err != nil {
			return false, nil, err
		}
		if _, found, err := unstructured.NestedString(asset, "asset_type"); !found || err != nil {
			return false, nil, err
		}
		if _, found, err := unstructured.NestedString(asset, "ancestry_path"); !found || err != nil {
			return false, nil, err
		}
		_, foundResource, err := unstructured.NestedMap(asset, "resource")
		if err != nil {
			return false, nil, err
		}
		_, foundIam, err := unstructured.NestedMap(asset, "iam_policy")
		if err != nil {
			return false, nil, err
		}
		if !foundIam && !foundResource {
			return false, nil, nil
		}
		if foundIam && foundResource {
			return false, nil, errors.Errorf("malformed asset has iam_policy and resource: %v", asset)
		}
		return true, asset, nil
	}
	return false, nil, nil
}

// handleAsset handles input from FCV assets as received via the gRPC interface.
func (g *GCPTarget) handleAsset(asset *validator.Asset) (bool, interface{}, error) {
	if asset.Resource == nil {
		return false, nil, errors.Errorf("forseti asset's resource field is nil %s", asset)
	}
	asset2.CleanStructValue(asset.Resource.Data)
	m := &jsonpb.Marshaler{
		OrigName: true,
	}
	var buf bytes.Buffer
	if err := m.Marshal(&buf, asset); err != nil {
		return false, nil, errors.Wrapf(err, "marshalling to json with asset %s: %v", asset.Name, asset)
	}
	var f interface{}
	err := json.Unmarshal(buf.Bytes(), &f)
	if err != nil {
		return false, nil, errors.Wrapf(err, "marshalling from json with asset %s: %v", asset.Name, asset)
	}
	return true, f, nil
}

// HandleViolation implements client.TargetHandler
func (g *GCPTarget) HandleViolation(result *types.Result) error {
	result.Resource = result.Review
	return nil
}

/*
cases
organizations/*
organizations/[0-9]+/*
organizations/[0-9]+/folders/*
organizations/[0-9]+/folders/[0-9]+/*
organizations/[0-9]+/folders/[0-9]+/projects/*
organizations/[0-9]+/folders/[0-9]+/projects/[0-9]+
folders/*
folders/[0-9]+/*
folders/[0-9]+/projects/*
folders/[0-9]+/projects/[0-9]+
projects/*
projects/[0-9]+
*/

const (
	organization = "organizations"
	folder       = "folders"
	project      = "projects"
)

const (
	stateStart   = "stateStart"
	stateFolder  = "stateFolder"
	stateProject = "stateProject"
)

var numberRegex = regexp.MustCompile(`^[0-9]+\*{0,2}$`)

// checkPathGlob
func checkPathGlob(expression string) error {
	// check for path components / numbers
	parts := strings.Split(expression, "/")
	state := stateStart
	for i := 0; i < len(parts); i++ {
		item := parts[i]
		switch {
		case item == organization:
			if state != stateStart {
				return errors.Errorf("unexpected %s element %d in %s", item, i, expression)
			}
			state = stateFolder
		case item == folder:
			if state != stateStart && state != stateFolder {
				return errors.Errorf("unexpected %s element %d in %s", item, i, expression)
			}
			state = stateFolder
		case item == project:
			state = stateProject
		case item == "*":
		case item == "**":
		case numberRegex.MatchString(item):
		default:
			return errors.Errorf("unexpected item %s element %d in %s", item, i, expression)
		}
	}
	return nil
}

func checkPathGlobs(rs []string) error {
	for idx, r := range rs {
		if err := checkPathGlob(r); err != nil {
			return errors.Wrapf(err, "idx: %d", idx)
		}
	}
	return nil
}

// ValidateConstraint implements client.TargetHandler
func (g *GCPTarget) ValidateConstraint(constraint *unstructured.Unstructured) error {
	targets, found, err := unstructured.NestedStringSlice(constraint.Object, "spec", "match", "target")
	if err != nil {
		return errors.Errorf("invalid spec.match.target: %s", err)
	}
	if found {
		if err := checkPathGlobs(targets); err != nil {
			return errors.Wrapf(err, "invalid glob in target")
		}
	}
	excludes, found, err := unstructured.NestedStringSlice(constraint.Object, "spec", "match", "exclude")
	if err != nil {
		return errors.Errorf("invalid spec.match.exclude: %s", err)
	}
	if found {
		if err := checkPathGlobs(excludes); err != nil {
			return errors.Wrapf(err, "invalid glob in exclude")
		}
	}
	return nil
}
