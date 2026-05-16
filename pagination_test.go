package smplkit_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	smplkit "github.com/smplkit/go-sdk/v3"
)

// fetchAllPageSize mirrors the value the runtime requests when walking
// every page; tests use the same constant to drive the multi-page exit.
const fetchAllPageSize = 1000

// pagedHandler returns a handler that serves repeating responses keyed by
// the requested page[number]. Each entry in pages becomes the response
// body for that 1-based page; pages past the end produce {"data":[]}.
//
// Records the page[number] / page[size] values the wrapper sent for
// every request in capturedQueries (one entry per call, ordered by
// arrival).
type pageRecorder struct {
	pageNumbers []string
	pageSizes   []string
}

func newPagedHandler(t *testing.T, path string, pages []string, rec *pageRecorder) http.HandlerFunc {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, path, r.URL.Path)
		q := r.URL.Query()
		rec.pageNumbers = append(rec.pageNumbers, q.Get("page[number]"))
		rec.pageSizes = append(rec.pageSizes, q.Get("page[size]"))

		pageNum, _ := strconv.Atoi(q.Get("page[number]"))
		if pageNum < 1 {
			pageNum = 1
		}
		idx := pageNum - 1

		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		if idx >= len(pages) {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		_, _ = w.Write([]byte(pages[idx]))
	})
}

// repeatJSON builds a data array with n items by repeating template,
// substituting {{i}} with each 1-based index.
func repeatJSON(template string, n int) string {
	parts := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		parts = append(parts, strings.ReplaceAll(template, "{{i}}", strconv.Itoa(i)))
	}
	return `{"data":[` + strings.Join(parts, ",") + `]}`
}

// newPaginationTestClient creates a client routed to the given handler.
// Mirrors newManagementTestClient — duplicated here to keep this file
// self-contained.
func newPaginationTestClient(t *testing.T, handler http.Handler) *smplkit.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := smplkit.NewClient(
		smplkit.Config{APIKey: "sk_test", Environment: "test", Service: "test-service", DisableTelemetry: true},
		smplkit.WithBaseURL(server.URL),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// ── Management List options pass through ─────────────────────────────────────

func TestConfigManagement_List_PaginationOptions(t *testing.T) {
	var rec pageRecorder
	handler := newPagedHandler(t, "/api/v1/configs", []string{`{"data":[{"id":"a","type":"config","attributes":{"name":"A","items":{},"environments":{}}}]}`}, &rec)
	client := newPaginationTestClient(t, handler)

	configs, err := client.Config().Management().List(context.Background(),
		smplkit.WithPageNumber(2), smplkit.WithPageSize(50))
	require.NoError(t, err)
	require.Len(t, configs, 0) // page 2 has no entries in our pages slice
	require.Len(t, rec.pageNumbers, 1)
	assert.Equal(t, "2", rec.pageNumbers[0])
	assert.Equal(t, "50", rec.pageSizes[0])
}

func TestFlagsManagement_List_PaginationOptions(t *testing.T) {
	var rec pageRecorder
	handler := newPagedHandler(t, "/api/v1/flags",
		[]string{`{"data":[{"id":"flag-a","type":"flag","attributes":{"name":"A","type":"boolean","default":true,"description":null,"environments":{}}}]}`},
		&rec)
	client := newPaginationTestClient(t, handler)

	flags, err := client.Flags().Management().List(context.Background(),
		smplkit.WithPageNumber(3), smplkit.WithPageSize(25))
	require.NoError(t, err)
	assert.Empty(t, flags) // page 3 is out of range
	require.Len(t, rec.pageNumbers, 1)
	assert.Equal(t, "3", rec.pageNumbers[0])
	assert.Equal(t, "25", rec.pageSizes[0])
}

func TestFlagsManagement_ListContextTypes_PaginationOptions(t *testing.T) {
	var rec pageRecorder
	handler := newPagedHandler(t, "/api/v1/context_types",
		[]string{`{"data":[]}`},
		&rec)
	client := newPaginationTestClient(t, handler)

	_, err := client.Flags().Management().ListContextTypes(context.Background(),
		smplkit.WithPageNumber(4), smplkit.WithPageSize(10))
	require.NoError(t, err)
	require.Len(t, rec.pageNumbers, 1)
	assert.Equal(t, "4", rec.pageNumbers[0])
	assert.Equal(t, "10", rec.pageSizes[0])
}

func TestFlagsManagement_ListContexts_PaginationOptions(t *testing.T) {
	var rec pageRecorder
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/contexts", r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "user", q.Get("filter[context_type]"))
		rec.pageNumbers = append(rec.pageNumbers, q.Get("page[number]"))
		rec.pageSizes = append(rec.pageSizes, q.Get("page[size]"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	client := newPaginationTestClient(t, handler)

	_, err := client.Flags().Management().ListContexts(context.Background(), "user",
		smplkit.WithPageNumber(5), smplkit.WithPageSize(7))
	require.NoError(t, err)
	require.Len(t, rec.pageNumbers, 1)
	assert.Equal(t, "5", rec.pageNumbers[0])
	assert.Equal(t, "7", rec.pageSizes[0])
}

func TestLoggingManagement_List_PaginationOptions(t *testing.T) {
	var rec pageRecorder
	handler := newPagedHandler(t, "/api/v1/loggers",
		[]string{`{"data":[]}`},
		&rec)
	client := newPaginationTestClient(t, handler)

	_, err := client.Logging().Management().List(context.Background(),
		smplkit.WithPageNumber(2), smplkit.WithPageSize(40))
	require.NoError(t, err)
	require.Len(t, rec.pageNumbers, 1)
	assert.Equal(t, "2", rec.pageNumbers[0])
	assert.Equal(t, "40", rec.pageSizes[0])
}

func TestLoggingManagement_ListGroups_PaginationOptions(t *testing.T) {
	var rec pageRecorder
	handler := newPagedHandler(t, "/api/v1/log_groups",
		[]string{`{"data":[]}`},
		&rec)
	client := newPaginationTestClient(t, handler)

	_, err := client.Logging().Management().ListGroups(context.Background(),
		smplkit.WithPageNumber(9), smplkit.WithPageSize(11))
	require.NoError(t, err)
	require.Len(t, rec.pageNumbers, 1)
	assert.Equal(t, "9", rec.pageNumbers[0])
	assert.Equal(t, "11", rec.pageSizes[0])
}

func TestEnvironmentsManagement_List_PaginationOptions(t *testing.T) {
	var rec pageRecorder
	handler := newPagedHandler(t, "/api/v1/environments",
		[]string{`{"data":[]}`},
		&rec)
	client := newPaginationTestClient(t, handler)

	_, err := client.Management().Environments().List(context.Background(),
		smplkit.WithPageNumber(2), smplkit.WithPageSize(3))
	require.NoError(t, err)
	require.Len(t, rec.pageNumbers, 1)
	assert.Equal(t, "2", rec.pageNumbers[0])
	assert.Equal(t, "3", rec.pageSizes[0])
}

func TestContextTypesManagement_List_PaginationOptions(t *testing.T) {
	var rec pageRecorder
	handler := newPagedHandler(t, "/api/v1/context_types",
		[]string{`{"data":[]}`},
		&rec)
	client := newPaginationTestClient(t, handler)

	_, err := client.Management().ContextTypes().List(context.Background(),
		smplkit.WithPageNumber(7), smplkit.WithPageSize(9))
	require.NoError(t, err)
	require.Len(t, rec.pageNumbers, 1)
	assert.Equal(t, "7", rec.pageNumbers[0])
	assert.Equal(t, "9", rec.pageSizes[0])
}

func TestContextsManagement_List_PaginationOptions(t *testing.T) {
	var rec pageRecorder
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/contexts", r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "account", q.Get("filter[context_type]"))
		rec.pageNumbers = append(rec.pageNumbers, q.Get("page[number]"))
		rec.pageSizes = append(rec.pageSizes, q.Get("page[size]"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	client := newPaginationTestClient(t, handler)

	_, err := client.Management().Contexts().List(context.Background(), "account",
		smplkit.WithPageNumber(8), smplkit.WithPageSize(12))
	require.NoError(t, err)
	require.Len(t, rec.pageNumbers, 1)
	assert.Equal(t, "8", rec.pageNumbers[0])
	assert.Equal(t, "12", rec.pageSizes[0])
}

func TestLoggersManagement_List_PaginationOptions(t *testing.T) {
	var rec pageRecorder
	handler := newPagedHandler(t, "/api/v1/loggers",
		[]string{`{"data":[]}`},
		&rec)
	client := newPaginationTestClient(t, handler)

	_, err := client.Management().Loggers().List(context.Background(),
		smplkit.WithPageNumber(4), smplkit.WithPageSize(8))
	require.NoError(t, err)
	require.Len(t, rec.pageNumbers, 1)
	assert.Equal(t, "4", rec.pageNumbers[0])
	assert.Equal(t, "8", rec.pageSizes[0])
}

func TestLogGroupsManagement_List_PaginationOptions(t *testing.T) {
	var rec pageRecorder
	handler := newPagedHandler(t, "/api/v1/log_groups",
		[]string{`{"data":[]}`},
		&rec)
	client := newPaginationTestClient(t, handler)

	_, err := client.Management().LogGroups().List(context.Background(),
		smplkit.WithPageNumber(6), smplkit.WithPageSize(13))
	require.NoError(t, err)
	require.Len(t, rec.pageNumbers, 1)
	assert.Equal(t, "6", rec.pageNumbers[0])
	assert.Equal(t, "13", rec.pageSizes[0])
}

// No options → no page[number] / page[size] query params sent; the
// server falls back to its own defaults.
func TestManagementList_NoOptions_OmitsPageParams(t *testing.T) {
	var capturedQuery string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	client := newPaginationTestClient(t, handler)

	_, err := client.Config().Management().List(context.Background())
	require.NoError(t, err)
	assert.NotContains(t, capturedQuery, "page%5Bnumber%5D")
	assert.NotContains(t, capturedQuery, "page%5Bsize%5D")
}

// ── Runtime fetch-all loops walk every page ──────────────────────────────────

// configResource emits one config resource with the given key.
const configResourceTemplate = `{"id":"cfg-{{i}}","type":"config","attributes":{"name":"Config {{i}}","items":{},"environments":{}}}`

func TestConfigClient_FetchAllConfigs_MultiPage(t *testing.T) {
	page1 := repeatJSON(configResourceTemplate, fetchAllPageSize)
	page2 := repeatJSON(configResourceTemplate, 3) // short page → terminates the loop

	var rec pageRecorder
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/configs" && r.Method == "GET" {
			q := r.URL.Query()
			rec.pageNumbers = append(rec.pageNumbers, q.Get("page[number]"))
			rec.pageSizes = append(rec.pageSizes, q.Get("page[size]"))
			pageNum, _ := strconv.Atoi(q.Get("page[number]"))
			w.WriteHeader(http.StatusOK)
			if pageNum == 1 {
				_, _ = w.Write([]byte(page1))
			} else {
				_, _ = w.Write([]byte(page2))
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/configs/") && r.Method == "GET" {
			id := strings.TrimPrefix(r.URL.Path, "/api/v1/configs/")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"config","attributes":{"name":"X","items":{},"environments":{}}}}`, id)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newPaginationTestClient(t, handler)

	// Refresh triggers fetchAllConfigs via ensureInit, then re-runs it on Refresh.
	// We only care that the loop walks both pages and stops on the short page.
	err := client.Config().Refresh(context.Background())
	require.NoError(t, err)

	// At least one ensureInit call + one Refresh call → expect 4 list calls
	// total (each iterates page 1 + page 2). The test only asserts the
	// loop behavior itself: pages 1 and 2 both observed, page sizes
	// always equal to fetchAllPageSize.
	require.GreaterOrEqual(t, len(rec.pageNumbers), 2)
	assert.Equal(t, "1", rec.pageNumbers[0])
	assert.Equal(t, "2", rec.pageNumbers[1])
	for _, ps := range rec.pageSizes {
		assert.Equal(t, strconv.Itoa(fetchAllPageSize), ps)
	}
}

const loggerResourceTemplate = `{"id":"lg-{{i}}","type":"logger","attributes":{"id":"lg-{{i}}","name":"Logger {{i}}","managed":true,"environments":{}}}`
const logGroupResourceTemplate = `{"id":"grp-{{i}}","type":"log_group","attributes":{"id":"grp-{{i}}","name":"Group {{i}}","environments":{}}}`

func TestLoggingClient_FetchAndCache_MultiPage(t *testing.T) {
	loggersP1 := repeatJSON(loggerResourceTemplate, fetchAllPageSize)
	loggersP2 := repeatJSON(loggerResourceTemplate, 2) // short page

	groupsP1 := repeatJSON(logGroupResourceTemplate, fetchAllPageSize)
	groupsP2 := repeatJSON(logGroupResourceTemplate, 1) // short page

	type loggerRec struct {
		pages []string
		sizes []string
	}
	var loggerR, groupR loggerRec

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		pageNum, _ := strconv.Atoi(q.Get("page[number]"))
		switch r.URL.Path {
		case "/api/v1/loggers":
			loggerR.pages = append(loggerR.pages, q.Get("page[number]"))
			loggerR.sizes = append(loggerR.sizes, q.Get("page[size]"))
			w.WriteHeader(http.StatusOK)
			if pageNum == 1 {
				_, _ = w.Write([]byte(loggersP1))
			} else {
				_, _ = w.Write([]byte(loggersP2))
			}
		case "/api/v1/log_groups":
			groupR.pages = append(groupR.pages, q.Get("page[number]"))
			groupR.sizes = append(groupR.sizes, q.Get("page[size]"))
			w.WriteHeader(http.StatusOK)
			if pageNum == 1 {
				_, _ = w.Write([]byte(groupsP1))
			} else {
				_, _ = w.Write([]byte(groupsP2))
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	client := newPaginationTestClient(t, handler)

	// Start triggers fetchAndCache which calls both fetch helpers.
	require.NoError(t, client.Logging().Start(context.Background()))

	require.Equal(t, []string{"1", "2"}, loggerR.pages)
	require.Equal(t, []string{"1", "2"}, groupR.pages)
	for _, ps := range loggerR.sizes {
		assert.Equal(t, strconv.Itoa(fetchAllPageSize), ps)
	}
	for _, ps := range groupR.sizes {
		assert.Equal(t, strconv.Itoa(fetchAllPageSize), ps)
	}
}

const flagResourceTemplate = `{"id":"flag-{{i}}","type":"flag","attributes":{"name":"Flag {{i}}","type":"boolean","default":true,"description":null,"environments":{}}}`

func TestFlagsClient_FetchFlagsList_MultiPage(t *testing.T) {
	flagsP1 := repeatJSON(flagResourceTemplate, fetchAllPageSize)
	flagsP2 := repeatJSON(flagResourceTemplate, 4) // short page

	var rec pageRecorder
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/flags" && r.Method == "GET":
			q := r.URL.Query()
			rec.pageNumbers = append(rec.pageNumbers, q.Get("page[number]"))
			rec.pageSizes = append(rec.pageSizes, q.Get("page[size]"))
			pageNum, _ := strconv.Atoi(q.Get("page[number]"))
			w.WriteHeader(http.StatusOK)
			if pageNum == 1 {
				_, _ = w.Write([]byte(flagsP1))
			} else {
				_, _ = w.Write([]byte(flagsP2))
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	client := newPaginationTestClient(t, handler)

	// Refresh triggers the runtime to walk pages via fetchAllFlags.
	err := client.Flags().Refresh(context.Background())
	require.NoError(t, err)

	// Both pages observed at least once; every call requested
	// fetchAllPageSize items.
	require.GreaterOrEqual(t, len(rec.pageNumbers), 2)
	assert.Equal(t, "1", rec.pageNumbers[0])
	assert.Equal(t, "2", rec.pageNumbers[1])
	for _, ps := range rec.pageSizes {
		assert.Equal(t, strconv.Itoa(fetchAllPageSize), ps)
	}
}

// Single-page exit: the loop terminates on the first page when the
// server returns fewer rows than requested.
func TestConfigClient_FetchAllConfigs_SinglePage(t *testing.T) {
	var rec pageRecorder
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/configs" && r.Method == "GET" {
			q := r.URL.Query()
			rec.pageNumbers = append(rec.pageNumbers, q.Get("page[number]"))
			rec.pageSizes = append(rec.pageSizes, q.Get("page[size]"))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(repeatJSON(configResourceTemplate, 3)))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/configs/") && r.Method == "GET" {
			id := strings.TrimPrefix(r.URL.Path, "/api/v1/configs/")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"type":"config","attributes":{"name":"X","items":{},"environments":{}}}}`, id)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	client := newPaginationTestClient(t, handler)

	err := client.Config().Refresh(context.Background())
	require.NoError(t, err)
	// Each fetch (ensureInit + Refresh) should be a single list call; in
	// total exactly two list calls, both for page 1.
	require.Len(t, rec.pageNumbers, 2)
	assert.Equal(t, "1", rec.pageNumbers[0])
	assert.Equal(t, "1", rec.pageNumbers[1])
}
