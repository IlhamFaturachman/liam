package antigravity

import (
	"fmt"
	"strings"
)

var unsupportedConstraints = []string{
	"minLength", "maxLength", "exclusiveMinimum", "exclusiveMaximum",
	"pattern", "minItems", "maxItems", "uniqueItems", "format",
	"default", "examples",
}

// cleanSchema performs deep JSON Schema cleaning for Gemini/Antigravity compatibility
// using native map[string]interface{} traversal without gjson/sjson.
func cleanSchema(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
	}

	// Phase 1: Conversions
	convertConstToEnum(schema)
	convertEnumValuesToStrings(schema)

	// Phase 2: Flattening
	mergeAllOf(schema)
	flattenAnyOfOneOf(schema)
	flattenTypeArrays(schema)

	// Phase 3: Keyword removal
	removeUnsupportedKeywords(schema)
	cleanupRequiredFields(schema)

	// Phase 4: Empty schema placeholder
	addEmptySchemaPlaceholder(schema, true)

	// Ensure type exists if properties exist
	if _, hasProps := schema["properties"]; hasProps {
		if _, hasType := schema["type"]; !hasType {
			schema["type"] = "object"
		}
	}

	return schema
}

func convertConstToEnum(schema map[string]interface{}) {
	if val, ok := schema["const"]; ok {
		if _, hasEnum := schema["enum"]; !hasEnum {
			schema["enum"] = []interface{}{val}
		}
	}
	for _, v := range schema {
		if m, ok := v.(map[string]interface{}); ok {
			convertConstToEnum(m)
		} else if arr, ok := v.([]interface{}); ok {
			for _, item := range arr {
				if m, ok := item.(map[string]interface{}); ok {
					convertConstToEnum(m)
				}
			}
		}
	}
}

func convertEnumValuesToStrings(schema map[string]interface{}) {
	if enumRaw, ok := schema["enum"]; ok {
		if enumArr, ok := enumRaw.([]interface{}); ok {
			strArr := make([]interface{}, 0, len(enumArr))
			for _, item := range enumArr {
				strArr = append(strArr, fmt.Sprintf("%v", item))
			}
			schema["enum"] = strArr
			schema["type"] = "string"
		}
	}
	for _, v := range schema {
		if m, ok := v.(map[string]interface{}); ok {
			convertEnumValuesToStrings(m)
		} else if arr, ok := v.([]interface{}); ok {
			for _, item := range arr {
				if m, ok := item.(map[string]interface{}); ok {
					convertEnumValuesToStrings(m)
				}
			}
		}
	}
}

func mergeAllOf(schema map[string]interface{}) {
	if allOfRaw, ok := schema["allOf"]; ok {
		if allOfArr, ok := allOfRaw.([]interface{}); ok {
			for _, itemRaw := range allOfArr {
				if item, ok := itemRaw.(map[string]interface{}); ok {
					// Merge properties
					if propsRaw, ok := item["properties"]; ok {
						if props, ok := propsRaw.(map[string]interface{}); ok {
							if targetPropsRaw, ok := schema["properties"]; ok {
								if targetProps, ok := targetPropsRaw.(map[string]interface{}); ok {
									for k, v := range props {
										targetProps[k] = v
									}
								}
							} else {
								schema["properties"] = props
							}
						}
					}
					// Merge required
					if reqRaw, ok := item["required"]; ok {
						if reqArr, ok := reqRaw.([]interface{}); ok {
							var targetReq []interface{}
							if existingReq, ok := schema["required"].([]interface{}); ok {
								targetReq = existingReq
							}
							for _, r := range reqArr {
								found := false
								for _, er := range targetReq {
									if r == er {
										found = true
										break
									}
								}
								if !found {
									targetReq = append(targetReq, r)
								}
							}
							schema["required"] = targetReq
						}
					}
				}
			}
		}
		delete(schema, "allOf")
	}

	for _, v := range schema {
		if m, ok := v.(map[string]interface{}); ok {
			mergeAllOf(m)
		} else if arr, ok := v.([]interface{}); ok {
			for _, item := range arr {
				if m, ok := item.(map[string]interface{}); ok {
					mergeAllOf(m)
				}
			}
		}
	}
}

func flattenAnyOfOneOf(schema map[string]interface{}) {
	for _, key := range []string{"anyOf", "oneOf"} {
		if arrRaw, ok := schema[key]; ok {
			if arr, ok := arrRaw.([]interface{}); ok && len(arr) > 0 {
				bestIdx := -1
				bestScore := -1
				for i, itemRaw := range arr {
					if item, ok := itemRaw.(map[string]interface{}); ok {
						score := 0
						t, _ := item["type"].(string)
						if t == "object" || item["properties"] != nil {
							score = 3
						} else if t == "array" || item["items"] != nil {
							score = 2
						} else if t != "" && t != "null" {
							score = 1
						}
						if score > bestScore {
							bestScore = score
							bestIdx = i
						}
					}
				}
				if bestIdx >= 0 {
					if bestItem, ok := arr[bestIdx].(map[string]interface{}); ok {
						for k, v := range bestItem {
							schema[k] = v
						}
					}
				}
			}
			delete(schema, key)
		}
	}

	for _, v := range schema {
		if m, ok := v.(map[string]interface{}); ok {
			flattenAnyOfOneOf(m)
		} else if arr, ok := v.([]interface{}); ok {
			for _, item := range arr {
				if m, ok := item.(map[string]interface{}); ok {
					flattenAnyOfOneOf(m)
				}
			}
		}
	}
}

func flattenTypeArrays(schema map[string]interface{}) {
	if typeRaw, ok := schema["type"]; ok {
		if typeArr, ok := typeRaw.([]interface{}); ok {
			firstType := "string"
			for _, t := range typeArr {
				if tStr, ok := t.(string); ok && tStr != "null" && tStr != "" {
					firstType = tStr
					break
				}
			}
			schema["type"] = firstType
		}
	}

	for _, v := range schema {
		if m, ok := v.(map[string]interface{}); ok {
			flattenTypeArrays(m)
		} else if arr, ok := v.([]interface{}); ok {
			for _, item := range arr {
				if m, ok := item.(map[string]interface{}); ok {
					flattenTypeArrays(m)
				}
			}
		}
	}
}

func removeUnsupportedKeywords(schema map[string]interface{}) {
	keywords := append(unsupportedConstraints,
		"$schema", "$defs", "definitions", "const", "$ref", "$id", "additionalProperties",
		"propertyNames", "patternProperties", "enumTitles", "prefill", "deprecated",
	)

	for _, key := range keywords {
		delete(schema, key)
	}

	// Remove x-* vendor extensions
	keysToDelete := []string{}
	for k := range schema {
		if strings.HasPrefix(k, "x-") {
			keysToDelete = append(keysToDelete, k)
		}
	}
	for _, k := range keysToDelete {
		delete(schema, k)
	}

	for _, v := range schema {
		if m, ok := v.(map[string]interface{}); ok {
			removeUnsupportedKeywords(m)
		} else if arr, ok := v.([]interface{}); ok {
			for _, item := range arr {
				if m, ok := item.(map[string]interface{}); ok {
					removeUnsupportedKeywords(m)
				}
			}
		}
	}
}

func cleanupRequiredFields(schema map[string]interface{}) {
	if reqRaw, ok := schema["required"]; ok {
		if reqArr, ok := reqRaw.([]interface{}); ok {
			if propsRaw, ok := schema["properties"]; ok {
				if props, ok := propsRaw.(map[string]interface{}); ok {
					var validReq []interface{}
					for _, r := range reqArr {
						if rStr, ok := r.(string); ok {
							if _, exists := props[rStr]; exists {
								validReq = append(validReq, r)
							}
						}
					}
					if len(validReq) == 0 {
						delete(schema, "required")
					} else {
						schema["required"] = validReq
					}
				}
			}
		}
	}

	for _, v := range schema {
		if m, ok := v.(map[string]interface{}); ok {
			cleanupRequiredFields(m)
		} else if arr, ok := v.([]interface{}); ok {
			for _, item := range arr {
				if m, ok := item.(map[string]interface{}); ok {
					cleanupRequiredFields(m)
				}
			}
		}
	}
}

func addEmptySchemaPlaceholder(schema map[string]interface{}, isRoot bool) {
	if t, ok := schema["type"].(string); ok && t == "object" {
		needsPlaceholder := false
		if propsRaw, ok := schema["properties"]; ok {
			if props, ok := propsRaw.(map[string]interface{}); ok {
				if len(props) == 0 {
					needsPlaceholder = true
				}
			} else {
				needsPlaceholder = true
			}
		} else {
			needsPlaceholder = true
		}

		if needsPlaceholder {
			schema["properties"] = map[string]interface{}{
				"reason": map[string]interface{}{
					"type":        "string",
					"description": "Brief explanation of why you are calling this tool",
				},
			}
			schema["required"] = []interface{}{"reason"}
		}
	}

	for _, v := range schema {
		if m, ok := v.(map[string]interface{}); ok {
			addEmptySchemaPlaceholder(m, false)
		} else if arr, ok := v.([]interface{}); ok {
			for _, item := range arr {
				if m, ok := item.(map[string]interface{}); ok {
					addEmptySchemaPlaceholder(m, false)
				}
			}
		}
	}
}
