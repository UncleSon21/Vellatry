package warehouse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"

	"github.com/UncleSon21/vellatry/internal/google"
)

// BigQuery is the production Warehouse. Tables are created on first write in the
// configured dataset (region australia-southeast1 for Australian data residency).
type BigQuery struct {
	Client         *bigquery.Client
	Dataset        string
	Retention      time.Duration // partition expiry; 0 keeps forever
	MaxBytesBilled int64         // per rollup query; refuses runaway scans
}

var searchSchema = bigquery.Schema{
	{Name: "date", Type: bigquery.DateFieldType, Required: true},
	{Name: "query", Type: bigquery.StringFieldType},
	{Name: "page", Type: bigquery.StringFieldType},
	{Name: "country", Type: bigquery.StringFieldType},
	{Name: "clicks", Type: bigquery.IntegerFieldType},
	{Name: "impressions", Type: bigquery.IntegerFieldType},
	{Name: "position", Type: bigquery.FloatFieldType},
}

var analyticsSchema = bigquery.Schema{
	{Name: "date", Type: bigquery.DateFieldType, Required: true},
	{Name: "channel", Type: bigquery.StringFieldType},
	{Name: "source", Type: bigquery.StringFieldType},
	{Name: "landing_page", Type: bigquery.StringFieldType},
	{Name: "sessions", Type: bigquery.IntegerFieldType},
	{Name: "users", Type: bigquery.IntegerFieldType},
	{Name: "key_events", Type: bigquery.FloatFieldType},
}

func tableName(kind, org string) string { return kind + "_" + strings.ReplaceAll(org, "-", "") }

// EnsureDataset creates the dataset in location (e.g. australia-southeast1) if needed.
func (b *BigQuery) EnsureDataset(ctx context.Context, location string) error {
	ds := b.Client.Dataset(b.Dataset)
	if _, err := ds.Metadata(ctx); err == nil || !isNotFound(err) {
		return err
	}
	err := ds.Create(ctx, &bigquery.DatasetMetadata{Location: location, Description: "Vellatry raw Search Console and GA4 facts, one table per tenant"})
	if isAlreadyExists(err) {
		return nil
	}
	return err
}

func (b *BigQuery) ensure(ctx context.Context, name string, schema bigquery.Schema, cluster []string) (*bigquery.Table, error) {
	t := b.Client.Dataset(b.Dataset).Table(name)
	_, err := t.Metadata(ctx)
	if err == nil {
		return t, nil
	}
	if !isNotFound(err) {
		return nil, err
	}
	err = t.Create(ctx, &bigquery.TableMetadata{
		Schema:           schema,
		TimePartitioning: &bigquery.TimePartitioning{Type: bigquery.DayPartitioningType, Field: "date", Expiration: b.Retention},
		Clustering:       &bigquery.Clustering{Fields: cluster},
	})
	if err != nil && !isAlreadyExists(err) {
		return nil, err
	}
	return t, nil
}

// replaceDay truncates and reloads one day partition in a single load job.
func (b *BigQuery) replaceDay(ctx context.Context, t *bigquery.Table, schema bigquery.Schema, day time.Time, ndjson []byte) error {
	partition := b.Client.Dataset(b.Dataset).Table(t.TableID + "$" + day.Format("20060102"))
	if len(ndjson) == 0 {
		// Nothing to load: empty the partition with a zero-row query into it.
		cols := make([]string, len(schema))
		for i, f := range schema {
			cols[i] = f.Name
		}
		q := b.Client.Query(fmt.Sprintf("SELECT %s FROM `%s.%s.%s` WHERE FALSE", strings.Join(cols, ", "), t.ProjectID, t.DatasetID, t.TableID))
		q.Dst = partition
		q.WriteDisposition = bigquery.WriteTruncate
		return runJob(ctx, q.Run)
	}
	src := bigquery.NewReaderSource(bytes.NewReader(ndjson))
	src.SourceFormat = bigquery.JSON
	src.Schema = schema
	loader := partition.LoaderFrom(src)
	loader.WriteDisposition = bigquery.WriteTruncate
	return runJob(ctx, loader.Run)
}

func runJob(ctx context.Context, run func(context.Context) (*bigquery.Job, error)) error {
	job, err := run(ctx)
	if err != nil {
		return err
	}
	status, err := job.Wait(ctx)
	if err != nil {
		return err
	}
	return status.Err()
}

func encode[T any](rows []T) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func (b *BigQuery) ReplaceSearchDay(ctx context.Context, org string, day time.Time, rows []google.SearchRow) error {
	t, err := b.ensure(ctx, tableName("search", org), searchSchema, []string{"query", "page"})
	if err != nil {
		return err
	}
	data, err := encode(rows)
	if err != nil {
		return err
	}
	return b.replaceDay(ctx, t, searchSchema, day, data)
}

func (b *BigQuery) ReplaceAnalyticsDay(ctx context.Context, org string, day time.Time, rows []google.AnalyticsRow) error {
	t, err := b.ensure(ctx, tableName("analytics", org), analyticsSchema, []string{"landing_page", "source"})
	if err != nil {
		return err
	}
	data, err := encode(rows)
	if err != nil {
		return err
	}
	return b.replaceDay(ctx, t, analyticsSchema, day, data)
}

func (b *BigQuery) query(ctx context.Context, sql string, params map[string]any) (*bigquery.RowIterator, error) {
	q := b.Client.Query(sql)
	q.MaxBytesBilled = b.MaxBytesBilled
	for k, v := range params {
		q.Parameters = append(q.Parameters, bigquery.QueryParameter{Name: k, Value: v})
	}
	return q.Read(ctx)
}

func (b *BigQuery) ref(kind, org string) string {
	return fmt.Sprintf("`%s.%s.%s`", b.Client.Project(), b.Dataset, tableName(kind, org))
}

func (b *BigQuery) SearchTop(ctx context.Context, org string, from, to time.Time, limit int) ([]Rollup, []Rollup, error) {
	var out [2][]Rollup
	for i, col := range []string{"query", "page"} {
		it, err := b.query(ctx, fmt.Sprintf(`
			SELECT %[1]s, SUM(clicks), SUM(impressions), SUM(position * impressions)
			FROM %[2]s WHERE date BETWEEN @from AND @to AND %[1]s IS NOT NULL
			GROUP BY %[1]s ORDER BY SUM(impressions) DESC LIMIT @limit`, col, b.ref("search", org)),
			map[string]any{"from": civil.DateOf(from), "to": civil.DateOf(to), "limit": limit})
		if isNotFound(err) {
			return nil, nil, nil
		}
		if err != nil {
			return nil, nil, err
		}
		for {
			var row []bigquery.Value
			err := it.Next(&row)
			if errors.Is(err, iterator.Done) {
				break
			}
			if err != nil {
				return nil, nil, err
			}
			out[i] = append(out[i], Rollup{Key: str(row[0]), Clicks: i64(row[1]), Impressions: i64(row[2]), PositionSum: f64(row[3])})
		}
	}
	return out[0], out[1], nil
}

func (b *BigQuery) QueryPages(ctx context.Context, org string, queries []string, from, to time.Time) ([]QueryPage, error) {
	if len(queries) == 0 {
		return nil, nil
	}
	it, err := b.query(ctx, fmt.Sprintf(`
		SELECT query, page, SUM(clicks), SUM(impressions) FROM %s
		WHERE date BETWEEN @from AND @to AND query IN UNNEST(@queries) AND page IS NOT NULL
		GROUP BY query, page ORDER BY SUM(impressions) DESC LIMIT 5000`, b.ref("search", org)),
		map[string]any{"from": civil.DateOf(from), "to": civil.DateOf(to), "queries": queries})
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []QueryPage
	for {
		var row []bigquery.Value
		err := it.Next(&row)
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, QueryPage{Query: str(row[0]), Page: str(row[1]), Clicks: i64(row[2]), Impressions: i64(row[3])})
	}
}

func (b *BigQuery) SearchDetailImpressions(ctx context.Context, org string, from, to time.Time) (map[string]int64, error) {
	it, err := b.query(ctx, fmt.Sprintf(`SELECT CAST(date AS STRING), SUM(impressions) FROM %s WHERE date BETWEEN @from AND @to GROUP BY date`, b.ref("search", org)),
		map[string]any{"from": civil.DateOf(from), "to": civil.DateOf(to)})
	if isNotFound(err) {
		return map[string]int64{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for {
		var row []bigquery.Value
		err := it.Next(&row)
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out[str(row[0])] = i64(row[1])
	}
}

func (b *BigQuery) LandingTop(ctx context.Context, org string, from, to time.Time, limit int) ([]Rollup, error) {
	it, err := b.query(ctx, fmt.Sprintf(`
		SELECT landing_page, SUM(sessions), SUM(key_events) FROM %s
		WHERE date BETWEEN @from AND @to AND landing_page IS NOT NULL
		GROUP BY landing_page ORDER BY SUM(sessions) DESC LIMIT @limit`, b.ref("analytics", org)),
		map[string]any{"from": civil.DateOf(from), "to": civil.DateOf(to), "limit": limit})
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Rollup
	for {
		var row []bigquery.Value
		err := it.Next(&row)
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, Rollup{Key: str(row[0]), Sessions: i64(row[1]), KeyEvents: f64(row[2])})
	}
}

func (b *BigQuery) DeleteTenant(ctx context.Context, org string) error {
	for _, kind := range []string{"search", "analytics"} {
		if err := b.Client.Dataset(b.Dataset).Table(tableName(kind, org)).Delete(ctx); err != nil && !isNotFound(err) {
			return err
		}
	}
	return nil
}

func isNotFound(err error) bool {
	var ge *googleapi.Error
	return errors.As(err, &ge) && ge.Code == http.StatusNotFound
}

func isAlreadyExists(err error) bool {
	var ge *googleapi.Error
	return errors.As(err, &ge) && ge.Code == http.StatusConflict
}

func str(v bigquery.Value) string {
	s, _ := v.(string)
	return s
}

func i64(v bigquery.Value) int64 {
	n, _ := v.(int64)
	return n
}

func f64(v bigquery.Value) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	}
	return 0
}
