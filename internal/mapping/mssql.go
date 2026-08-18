package mapping

import (
	"fmt"
	"regexp"
	"strings"

	"ms2pg/internal/catalog"
	"ms2pg/internal/sqlrewrite"
)

var (
	defaultCurrentTimePattern    = regexp.MustCompile(`(?i)\b(GETDATE|SYSDATETIME|CURRENT_TIMESTAMP)\b\s*(\(\s*\))?`)
	defaultCurrentUTCTimePattern = regexp.MustCompile(`(?i)\b(GETUTCDATE|SYSUTCDATETIME)\b\s*(\(\s*\))?`)
	defaultCurrentOffsetPattern  = regexp.MustCompile(`(?i)\bSYSDATETIMEOFFSET\b\s*(\(\s*\))?`)
	defaultNewIDPattern          = regexp.MustCompile(`(?i)\b(NEWID|NEWSEQUENTIALID)\b\s*(\(\s*\))?`)
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

	if column.Identity && !isIntegerType(baseType) {
		return "", fmt.Errorf("unsupported identity source type %q", column.SourceType)
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
		if baseType == "smallmoney" {
			return "numeric(10,4)", nil
		}
		return "numeric(19,4)", nil
	case "char", "varchar":
		return mapCharacterType(baseType, column.Length), nil
	case "nchar", "nvarchar":
		length := column.Length
		if length > 0 {
			length /= 2
		}
		return mapCharacterType(strings.TrimPrefix(baseType, "n"), length), nil
	case "text", "ntext":
		return "text", nil
	case "binary", "varbinary", "image", "rowversion", "timestamp":
		return "bytea", nil
	case "uniqueidentifier":
		return "uuid", nil
	case "date":
		return "date", nil
	case "time":
		return temporalType("time", column.Scale), nil
	case "datetime", "datetime2", "smalldatetime":
		if baseType == "datetime2" {
			return temporalType("timestamp", column.Scale), nil
		}
		return "timestamp", nil
	case "datetimeoffset":
		return temporalType("timestamptz", column.Scale), nil
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

func mapCharacterType(baseType string, length int64) string {
	if length <= 0 {
		return "text"
	}
	if baseType == "char" {
		return fmt.Sprintf("character(%d)", length)
	}
	return fmt.Sprintf("character varying(%d)", length)
}

func temporalType(baseType string, precision int64) string {
	return fmt.Sprintf("%s(%d)", baseType, min(max(precision, 0), 6))
}

func mapDefault(defaultExpr string) string {
	trimmed := unwrapOuterParens(strings.TrimSpace(defaultExpr))
	if trimmed == "" {
		return ""
	}

	protected := sqlrewrite.Protect(trimmed, true)
	normalized := protected.SQL
	normalized = defaultCurrentTimePattern.ReplaceAllString(normalized, "CURRENT_TIMESTAMP")
	normalized = defaultCurrentUTCTimePattern.ReplaceAllString(normalized, "(CURRENT_TIMESTAMP AT TIME ZONE 'UTC')")
	normalized = defaultCurrentOffsetPattern.ReplaceAllString(normalized, "CURRENT_TIMESTAMP")
	normalized = defaultNewIDPattern.ReplaceAllString(normalized, "gen_random_uuid()")
	return protected.Restore(normalized)
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
