package mapping

import (
	"fmt"
	"regexp"
	"strings"

	"ms2pg/internal/catalog"
)

var (
	defaultUnicodeStringPattern  = regexp.MustCompile(`(?i)\bN'`)
	defaultCurrentTimePattern    = regexp.MustCompile(`(?i)\b(GETDATE|SYSDATETIME|CURRENT_TIMESTAMP)\s*(\(\s*\))?`)
	defaultCurrentUTCTimePattern = regexp.MustCompile(`(?i)\b(GETUTCDATE|SYSUTCDATETIME|SYSDATETIMEOFFSET)\s*(\(\s*\))?`)
	defaultNewIDPattern          = regexp.MustCompile(`(?i)\b(NEWID|NEWSEQUENTIALID)\s*(\(\s*\))?`)
)

func Apply(table *catalog.Table) error {
	for _, column := range table.Columns {
		targetType, err := mapType(column)
		if err != nil {
			return fmt.Errorf("%s.%s.%s: %w", table.Schema, table.Name, column.Name, err)
		}
		column.TargetType = targetType
		column.Default = mapDefault(column.Default)
	}
	for _, defaultConstraint := range table.DefaultConstraints {
		defaultConstraint.Definition = mapDefault(defaultConstraint.Definition)
	}
	return nil
}

func mapType(column *catalog.Column) (string, error) {
	baseType := strings.ToLower(column.SourceType)

	if column.Identity && isIntegerType(baseType) {
		return "bigint", nil
	}

	switch baseType {
	case "bigint":
		return "bigint", nil
	case "int":
		return "integer", nil
	case "smallint":
		return "smallint", nil
	case "tinyint":
		return "smallint", nil
	case "bit":
		return "boolean", nil
	case "decimal", "numeric":
		if column.Precision > 0 {
			return fmt.Sprintf("numeric(%d,%d)", column.Precision, column.Scale), nil
		}
		return "numeric", nil
	case "float":
		if column.Precision > 0 && column.Precision <= 24 {
			return "real", nil
		}
		return "double precision", nil
	case "real":
		return "real", nil
	case "money", "smallmoney":
		return "numeric(19,4)", nil
	case "char", "nchar", "varchar", "nvarchar", "text", "ntext":
		return "text", nil
	case "binary", "varbinary", "image", "rowversion", "timestamp":
		return "bytea", nil
	case "uniqueidentifier":
		return "uuid", nil
	case "date":
		return "date", nil
	case "time":
		return "time", nil
	case "datetime", "datetime2", "smalldatetime":
		return "timestamp", nil
	case "datetimeoffset":
		return "timestamptz", nil
	case "xml":
		return "xml", nil
	case "hierarchyid", "geography", "geometry":
		return "bytea", nil
	case "sql_variant":
		return "text", nil
	default:
		return "", fmt.Errorf("unsupported source type %q", column.SourceType)
	}
}

func mapDefault(defaultExpr string) string {
	trimmed := unwrapOuterParens(strings.TrimSpace(defaultExpr))
	if trimmed == "" {
		return ""
	}

	normalized := trimmed
	normalized = defaultUnicodeStringPattern.ReplaceAllString(normalized, `'`)
	normalized = defaultCurrentTimePattern.ReplaceAllString(normalized, "CURRENT_TIMESTAMP")
	normalized = defaultCurrentUTCTimePattern.ReplaceAllString(normalized, "(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')")
	normalized = defaultNewIDPattern.ReplaceAllString(normalized, "gen_random_uuid()")
	return normalized
}

func unwrapOuterParens(value string) string {
	for len(value) >= 2 && value[0] == '(' && value[len(value)-1] == ')' {
		depth := 0
		balanced := true
		for index, ch := range value {
			switch ch {
			case '(':
				depth++
			case ')':
				depth--
				if depth < 0 {
					balanced = false
				}
				if depth == 0 && index != len(value)-1 {
					balanced = false
				}
			}
			if !balanced {
				break
			}
		}
		if !balanced || depth != 0 {
			break
		}
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	return value
}

func isIntegerType(sourceType string) bool {
	switch sourceType {
	case "bigint", "int", "smallint", "tinyint":
		return true
	default:
		return false
	}
}
