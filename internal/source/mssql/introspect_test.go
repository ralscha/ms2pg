package mssql

import (
	"testing"
	"time"

	"ms2pg/internal/catalog"
)

func TestSelectExpressionUsesMSSQLTemporalFormatting(t *testing.T) {
	tests := []struct {
		name   string
		column *catalog.Column
		want   string
	}{
		{
			name:   "datetime2",
			column: &catalog.Column{Name: "created_at", SourceType: "datetime2"},
			want:   "CONVERT(varchar(30), [created_at], 126) AS [created_at]",
		},
		{
			name:   "datetimeoffset",
			column: &catalog.Column{Name: "created_at_utc", SourceType: "datetimeoffset"},
			want:   "CONVERT(varchar(35), [created_at_utc], 127) AS [created_at_utc]",
		},
		{
			name:   "time",
			column: &catalog.Column{Name: "start_at", SourceType: "time"},
			want:   "CONVERT(varchar(30), [start_at], 114) AS [start_at]",
		},
		{
			name:   "plain",
			column: &catalog.Column{Name: "name", SourceType: "nvarchar"},
			want:   "[name] AS [name]",
		},
		{
			name:   "money",
			column: &catalog.Column{Name: "price", SourceType: "money"},
			want:   "CAST([price] AS decimal(19,4)) AS [price]",
		},
		{
			name:   "smallmoney",
			column: &catalog.Column{Name: "fee", SourceType: "smallmoney"},
			want:   "CAST([fee] AS decimal(19,4)) AS [fee]",
		},
		{
			name:   "sql_variant",
			column: &catalog.Column{Name: "val", SourceType: "sql_variant"},
			want:   "CAST([val] AS nvarchar(max)) AS [val]",
		},
	}

	for _, test := range tests {
		if got := selectExpression(test.column); got != test.want {
			t.Fatalf("%s selectExpression() = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestNormalizeTemporalValueParsesTemporalTypes(t *testing.T) {
	tests := []struct {
		name   string
		column *catalog.Column
		value  string
		want   any
		wantOK bool
	}{
		{
			name:   "timestamp",
			column: &catalog.Column{TargetType: "timestamp"},
			value:  "2026-05-08T11:12:13.1234567",
			want:   time.Date(2026, 5, 8, 11, 12, 13, 123456700, time.UTC),
			wantOK: true,
		},
		{
			name:   "timestamptz",
			column: &catalog.Column{TargetType: "timestamptz"},
			value:  "2026-05-08T11:12:13.1234567+02:00",
			want:   time.Date(2026, 5, 8, 11, 12, 13, 123456700, time.FixedZone("", 2*60*60)),
			wantOK: true,
		},
		{
			name:   "date",
			column: &catalog.Column{TargetType: "date"},
			value:  "2026-05-08",
			want:   time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC),
			wantOK: true,
		},
		{
			name:   "time",
			column: &catalog.Column{TargetType: "time"},
			value:  "11:12:13:1234567",
			want:   "11:12:13.123456",
			wantOK: true,
		},
		{
			name:   "invalid",
			column: &catalog.Column{TargetType: "timestamp"},
			value:  "not-a-timestamp",
			want:   nil,
			wantOK: false,
		},
	}

	for _, test := range tests {
		got, ok := normalizeTemporalValue(test.column, test.value)
		if ok != test.wantOK {
			t.Fatalf("%s normalizeTemporalValue() ok = %v, want %v", test.name, ok, test.wantOK)
		}
		if !test.wantOK {
			if got != nil {
				t.Fatalf("%s normalizeTemporalValue() = %#v, want nil", test.name, got)
			}
			continue
		}
		if !sameTemporalValue(got, test.want) {
			t.Fatalf("%s normalizeTemporalValue() = %#v, want %#v", test.name, got, test.want)
		}
	}
}

func sameTemporalValue(got any, want any) bool {
	gotTime, gotIsTime := got.(time.Time)
	wantTime, wantIsTime := want.(time.Time)
	if gotIsTime || wantIsTime {
		return gotIsTime && wantIsTime && gotTime.Equal(wantTime)
	}

	gotString, gotIsString := got.(string)
	wantString, wantIsString := want.(string)
	if gotIsString || wantIsString {
		return gotIsString && wantIsString && gotString == wantString
	}

	return got == want
}
