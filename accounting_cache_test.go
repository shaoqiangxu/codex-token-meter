package main

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"
)

type pricingReadProbe struct {
	sqlReader
	ledgerReads int
}

func (p *pricingReadProbe) Query(query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, "FROM usage_events") && strings.Contains(query, "output_tokens") {
		p.ledgerReads++
	}
	return p.sqlReader.Query(query, args...)
}

func TestPricingCacheBeyondLegacyCeilingKeepsExactWarmUpdates(t *testing.T) {
	s, done := testServerDB(t)
	defer done()
	at := time.Now().UTC().Add(-time.Minute)
	// Synthetic numeric records only; cross the actual production failure
	// threshold, including mixed long-context and cache-visibility cases.
	_, err := s.db.Exec(`WITH RECURSIVE numbers(n) AS (
		SELECT 1 UNION ALL SELECT n+1 FROM numbers WHERE n<100001
	), valueset AS (
		SELECT n,CASE WHEN n%7=0 THEN 300000 ELSE 7500 END input FROM numbers
	)
	INSERT INTO usage_events(event_id,host_id,conversation_id,source_file_id,byte_offset,event_type,timestamp,
		raw_input_tokens,raw_cached_input_tokens,raw_cache_write_input_tokens,raw_output_tokens,raw_reasoning_output_tokens,raw_total_tokens,
		input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,reasoning_output_tokens,total_tokens,cache_write_visible,data_quality,parser_version,created_at)
	SELECT 'cache-limit-'||n,'h','synthetic-cache','synthetic-cache',n,'exact_usage',?,
		input,1000,0,20,5,input+20,input,1000,0,20,5,input+20,n%13<>0,'EXACT','synthetic',? FROM valueset`, at.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	probe := &pricingReadProbe{sqlReader: s.db}
	s.read = probe
	s.accounting = &accountingCache{}
	start, end := at.Add(-time.Hour), at.Add(time.Hour)
	for i := 0; i < 2; i++ {
		got, gotSessions := s.cachedRangeCosts(start, end)
		want, wantSessions := rangeCosts(s.db, start, end)
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotSessions, wantSessions) {
			t.Fatal("large-ledger cached pricing differs from historical reference")
		}
	}
	if probe.ledgerReads != 1 {
		t.Fatalf("warm update reread the entire price ledger: reads=%d", probe.ledgerReads)
	}
	// A newly appended exact event must be included once, including a source
	// timestamp older than cached events rather than assuming arrival order.
	_, err = s.db.Exec(`INSERT INTO usage_events(event_id,host_id,conversation_id,source_file_id,byte_offset,event_type,timestamp,
		raw_input_tokens,raw_cached_input_tokens,raw_cache_write_input_tokens,raw_output_tokens,raw_reasoning_output_tokens,raw_total_tokens,
		input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,reasoning_output_tokens,total_tokens,cache_write_visible,data_quality,parser_version,created_at)
		VALUES('late-cache-event','h','late-synthetic','late-synthetic',1,'exact_usage',?,101,0,0,2,0,103,101,0,0,2,0,103,1,'EXACT','synthetic',?)`, at.Add(-time.Second).Format(time.RFC3339Nano), at.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	got, gotSessions := s.cachedRangeCosts(start, end)
	want, wantSessions := rangeCosts(s.db, start, end)
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotSessions, wantSessions) {
		t.Fatal("late appended usage changed cached historical pricing")
	}
	if probe.ledgerReads != 2 {
		t.Fatal("new event was not read incrementally")
	}
}
