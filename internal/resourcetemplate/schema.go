/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package resourcetemplate

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/kube-openapi/pkg/spec3"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"k8s.io/utils/ptr"
)

// Schema sources recorded in the TemplateSchemaSourceAnnotation of generated CRDs.
const (
	// SchemaSourceCRD means spec.template was copied from the source CustomResourceDefinition.
	SchemaSourceCRD = "crd"
	// SchemaSourceOpenAPI means spec.template was derived from the API server's OpenAPI v3 document.
	SchemaSourceOpenAPI = "openapi"
	// SchemaSourceNone means spec.template accepts any fields without validation.
	SchemaSourceNone = "none"
)

const componentsSchemasPrefix = "#/components/schemas/"

// JSON schema type names and map types used while converting.
const (
	typeObject  = "object"
	typeArray   = "array"
	typeString  = "string"
	typeInteger = "integer"
	typeNumber  = "number"
	typeBoolean = "boolean"

	mapTypeAtomic   = "atomic"
	mapTypeGranular = "granular"
	listTypeAtomic  = "atomic"
	listTypeSet     = "set"
	listTypeMap     = "map"
)

// Extensions that CustomResourceDefinition schemas accept. Any other x-kubernetes-* extension found in an
// OpenAPI v3 document (patch strategies, group-version-kind, unions, ...) is dropped.
const (
	extListType       = "x-kubernetes-list-type"
	extListMapKeys    = "x-kubernetes-list-map-keys"
	extMapType        = "x-kubernetes-map-type"
	extIntOrString    = "x-kubernetes-int-or-string"
	extPreserveFields = "x-kubernetes-preserve-unknown-fields"
	extGVK            = "x-kubernetes-group-version-kind"
)

// preserveUnknownObject is the schema used wherever a precise schema cannot be expressed as a structural
// CRD schema: recursive types, untyped oneOf/anyOf, RawExtension, and the fallback template schema.
func preserveUnknownObject() *apiextensionsv1.JSONSchemaProps {
	return &apiextensionsv1.JSONSchemaProps{
		Type:                   typeObject,
		XPreserveUnknownFields: ptr.To(true),
	}
}

// FallbackTemplateSchema returns the spec.template schema used when no schema can be derived for the source
// kind: an object that accepts any fields. The controller still checks apiVersion and kind when it renders.
func FallbackTemplateSchema() *apiextensionsv1.JSONSchemaProps {
	return preserveUnknownObject()
}

// TemplateSchemaFromRoot turns a root schema of the source kind (from its CRD or the OpenAPI document) into the
// schema of spec.template: apiVersion and kind become plain strings, metadata becomes an open object, and status
// is dropped because the controller never copies it to the created resource. The input is modified and returned.
//
// spec.template is deliberately NOT marked x-kubernetes-embedded-resource: that would make the API server
// validate the template's metadata as real ObjectMeta, rejecting Go template expressions in labels and
// annotations ("{{ .Branch }}" is not a valid label value). The rendered resource is validated by the API
// server when the controller creates it, and the controller rejects a template whose apiVersion/kind do not
// match the source kind.
func TemplateSchemaFromRoot(s *apiextensionsv1.JSONSchemaProps) *apiextensionsv1.JSONSchemaProps {
	if s == nil {
		return FallbackTemplateSchema()
	}
	s.Type = typeObject
	s.XEmbeddedResource = false
	s.Ref = nil
	s.Nullable = false
	s.Default = nil
	if s.Properties == nil {
		if !ptr.Deref(s.XPreserveUnknownFields, false) {
			s.XPreserveUnknownFields = ptr.To(true)
		}
		return s
	}
	delete(s.Properties, "status")
	s.Required = slices.DeleteFunc(s.Required, func(r string) bool { return r == "status" })
	for _, name := range []string{"apiVersion", "kind"} {
		prop := s.Properties[name]
		s.Properties[name] = apiextensionsv1.JSONSchemaProps{Type: typeString, Description: prop.Description}
	}
	meta := s.Properties["metadata"]
	s.Properties["metadata"] = apiextensionsv1.JSONSchemaProps{
		Type:                   typeObject,
		Description:            meta.Description,
		XPreserveUnknownFields: ptr.To(true),
	}
	return s
}

// SchemaFromCRD returns a copy of the openAPIV3Schema of version in crd, or nil when that version has no schema.
func SchemaFromCRD(crd *apiextensionsv1.CustomResourceDefinition, version string) *apiextensionsv1.JSONSchemaProps {
	for i := range crd.Spec.Versions {
		v := &crd.Spec.Versions[i]
		if v.Name != version || v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
			continue
		}
		return v.Schema.OpenAPIV3Schema.DeepCopy()
	}
	return nil
}

// SchemaFromOpenAPIV3 parses an OpenAPI v3 document (as served under /openapi/v3/<path>) and returns the
// structural CRD schema of the component whose x-kubernetes-group-version-kind matches gvk. $refs are
// inlined, recursion is cut with x-kubernetes-preserve-unknown-fields, and extensions CRDs do not accept are
// dropped. It returns an error when the document cannot be parsed or has no component for gvk.
func SchemaFromOpenAPIV3(document []byte, gvk schema.GroupVersionKind) (*apiextensionsv1.JSONSchemaProps, error) {
	var doc spec3.OpenAPI
	if err := json.Unmarshal(document, &doc); err != nil {
		return nil, fmt.Errorf("parse OpenAPI v3 document: %w", err)
	}
	if doc.Components == nil {
		return nil, fmt.Errorf("OpenAPI v3 document has no components for %s", gvk)
	}
	for name, s := range doc.Components.Schemas {
		if s == nil || !schemaHasGVK(s, gvk) {
			continue
		}
		c := &converter{components: doc.Components.Schemas}
		return c.convert(s, map[string]bool{name: true}), nil
	}
	return nil, fmt.Errorf("OpenAPI v3 document has no schema for %s", gvk)
}

// schemaHasGVK reports whether s carries an x-kubernetes-group-version-kind entry equal to gvk.
func schemaHasGVK(s *spec.Schema, gvk schema.GroupVersionKind) bool {
	raw, ok := s.Extensions[extGVK]
	if !ok {
		return false
	}
	entries, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, e := range entries {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		group, _ := m["group"].(string)
		version, _ := m["version"].(string)
		kind, _ := m["kind"].(string)
		if group == gvk.Group && version == gvk.Version && kind == gvk.Kind {
			return true
		}
	}
	return false
}

// converter inlines $refs from an OpenAPI v3 components map into a structural CRD schema.
type converter struct {
	components map[string]*spec.Schema
}

// convert translates s. stack holds the component names currently being expanded so recursive types are cut.
func (c *converter) convert(s *spec.Schema, stack map[string]bool) *apiextensionsv1.JSONSchemaProps {
	if s == nil {
		return preserveUnknownObject()
	}

	// Kubernetes encodes typed references as {"allOf": [{"$ref": ...}], "description": ..., "default": {}}.
	if ref, ok := singleRef(s); ok {
		out := c.resolveRef(ref, stack)
		if s.Description != "" {
			out.Description = s.Description
		}
		return out
	}
	if ref := s.Ref.String(); ref != "" {
		return c.resolveRef(ref, stack)
	}

	out := &apiextensionsv1.JSONSchemaProps{Description: s.Description}
	typ := ""
	if len(s.Type) > 0 {
		typ = s.Type[0]
	}

	if typ == "" {
		return untypedSchema(s, out)
	}

	out.Type = typ
	switch typ {
	case typeObject:
		c.convertObject(s, out, stack)
	case typeArray:
		c.convertArray(s, out, stack)
	default:
		convertScalar(s, out)
	}
	out.Nullable = s.Nullable
	return out
}

// untypedSchema handles OpenAPI nodes without a type: int-or-string unions become x-kubernetes-int-or-string,
// everything else (Quantity's string-or-number, RawExtension, unknown unions) preserves unknown fields.
func untypedSchema(s *spec.Schema, out *apiextensionsv1.JSONSchemaProps) *apiextensionsv1.JSONSchemaProps {
	if flag, _ := s.Extensions[extIntOrString].(bool); flag || isIntOrStringUnion(s) {
		out.XIntOrString = true
		return out
	}
	out.XPreserveUnknownFields = ptr.To(true)
	return out
}

// isIntOrStringUnion reports whether the oneOf/anyOf members of s are exactly a string and an integer.
func isIntOrStringUnion(s *spec.Schema) bool {
	members := s.OneOf
	if len(members) == 0 {
		members = s.AnyOf
	}
	if len(members) != 2 {
		return false
	}
	var hasString, hasInteger bool
	for i := range members {
		if len(members[i].Type) != 1 {
			return false
		}
		switch members[i].Type[0] {
		case typeString:
			hasString = true
		case typeInteger:
			hasInteger = true
		default:
			return false
		}
	}
	return hasString && hasInteger
}

// singleRef reports whether s is the Kubernetes "allOf with one $ref" wrapper and returns that $ref.
func singleRef(s *spec.Schema) (string, bool) {
	if len(s.AllOf) != 1 || len(s.Type) > 0 || len(s.Properties) > 0 {
		return "", false
	}
	ref := s.AllOf[0].Ref.String()
	return ref, ref != ""
}

// resolveRef inlines the component named by ref. A component already on the stack (recursive type) or one
// that is missing from the document becomes an object that preserves unknown fields.
func (c *converter) resolveRef(ref string, stack map[string]bool) *apiextensionsv1.JSONSchemaProps {
	name := strings.TrimPrefix(ref, componentsSchemasPrefix)
	target, ok := c.components[name]
	if !ok || target == nil || stack[name] {
		return preserveUnknownObject()
	}
	stack[name] = true
	out := c.convert(target, stack)
	delete(stack, name)
	return out
}

func (c *converter) convertObject(s *spec.Schema, out *apiextensionsv1.JSONSchemaProps, stack map[string]bool) {
	if len(s.Properties) > 0 {
		out.Properties = make(map[string]apiextensionsv1.JSONSchemaProps, len(s.Properties))
		for name := range s.Properties {
			prop := s.Properties[name]
			out.Properties[name] = *c.convert(&prop, stack)
		}
		out.Required = slices.Clone(s.Required)
	} else if s.AdditionalProperties != nil && s.AdditionalProperties.Schema != nil {
		// properties and additionalProperties are mutually exclusive in a structural schema; properties win.
		out.AdditionalProperties = &apiextensionsv1.JSONSchemaPropsOrBool{
			Allows: true,
			Schema: c.convert(s.AdditionalProperties.Schema, stack),
		}
	} else {
		out.XPreserveUnknownFields = ptr.To(true)
	}
	if mapType, ok := s.Extensions[extMapType].(string); ok && (mapType == mapTypeAtomic || mapType == mapTypeGranular) {
		out.XMapType = ptr.To(mapType)
	}
	out.MinProperties = s.MinProperties
	out.MaxProperties = s.MaxProperties
}

func (c *converter) convertArray(s *spec.Schema, out *apiextensionsv1.JSONSchemaProps, stack map[string]bool) {
	var items *apiextensionsv1.JSONSchemaProps
	if s.Items != nil && s.Items.Schema != nil {
		items = c.convert(s.Items.Schema, stack)
	} else {
		items = preserveUnknownObject()
	}
	out.MinItems = s.MinItems
	out.MaxItems = s.MaxItems
	convertListExtensions(s, out, items)
	out.Items = &apiextensionsv1.JSONSchemaPropsOrArray{Schema: items}
}

// convertListExtensions carries x-kubernetes-list-type (and its map keys) over when the result stays valid
// for a CRD: map keys must be required or defaulted on the item schema, and set items that are objects must
// be atomic.
func convertListExtensions(s *spec.Schema, out *apiextensionsv1.JSONSchemaProps, items *apiextensionsv1.JSONSchemaProps) {
	listType, ok := s.Extensions[extListType].(string)
	if !ok {
		return
	}
	switch listType {
	case listTypeAtomic:
		out.XListType = ptr.To(listType)
	case listTypeSet:
		if items.Type == typeObject && ptr.Deref(items.XMapType, "") != mapTypeAtomic {
			items.XMapType = ptr.To(mapTypeAtomic)
		}
		out.XListType = ptr.To(listType)
	case listTypeMap:
		keys := stringSlice(s.Extensions[extListMapKeys])
		if items.Type != typeObject || len(keys) == 0 || !mapKeysValid(items, keys) {
			return
		}
		out.XListType = ptr.To(listType)
		out.XListMapKeys = keys
	default:
	}
}

// mapKeysValid reports whether every list map key is a scalar item property that is required or has a default.
func mapKeysValid(items *apiextensionsv1.JSONSchemaProps, keys []string) bool {
	for _, key := range keys {
		prop, ok := items.Properties[key]
		if !ok {
			return false
		}
		switch prop.Type {
		case typeString, typeInteger, typeNumber, typeBoolean:
		default:
			return false
		}
		if !slices.Contains(items.Required, key) && prop.Default == nil {
			return false
		}
	}
	return true
}

func stringSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// convertScalar copies the validations and the default of a string, integer, number, or boolean node.
// Defaults of object and array nodes are never copied: the API server validates CRD defaults against the
// schema, and the "default: {}" Kubernetes stamps on struct fields would not pass required-field checks.
func convertScalar(s *spec.Schema, out *apiextensionsv1.JSONSchemaProps) {
	out.Format = s.Format
	out.Pattern = s.Pattern
	out.Minimum = s.Minimum
	out.Maximum = s.Maximum
	out.ExclusiveMinimum = s.ExclusiveMinimum
	out.ExclusiveMaximum = s.ExclusiveMaximum
	out.MinLength = s.MinLength
	out.MaxLength = s.MaxLength
	out.MultipleOf = s.MultipleOf
	for _, e := range s.Enum {
		if raw, err := json.Marshal(e); err == nil {
			out.Enum = append(out.Enum, apiextensionsv1.JSON{Raw: raw})
		}
	}
	if s.Default != nil {
		if raw, err := json.Marshal(s.Default); err == nil {
			out.Default = &apiextensionsv1.JSON{Raw: raw}
		}
	}
}
