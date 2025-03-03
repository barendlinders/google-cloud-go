/*
Copyright 2017 Google LLC

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

package spanner

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"cloud.google.com/go/spanner/internal/benchserver"
	. "cloud.google.com/go/spanner/internal/testutil"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	gstatus "google.golang.org/grpc/status"
)

func setupMockedTestServer(t *testing.T) (server *MockedSpannerInMemTestServer, client *Client, teardown func()) {
	return setupMockedTestServerWithConfig(t, ClientConfig{})
}

func setupMockedTestServerWithConfig(t *testing.T, config ClientConfig) (server *MockedSpannerInMemTestServer, client *Client, teardown func()) {
	return setupMockedTestServerWithConfigAndClientOptions(t, config, []option.ClientOption{})
}

func setupMockedTestServerWithConfigAndClientOptions(t *testing.T, config ClientConfig, clientOptions []option.ClientOption) (server *MockedSpannerInMemTestServer, client *Client, teardown func()) {
	server, opts, serverTeardown := NewMockedSpannerInMemTestServer(t)
	opts = append(opts, clientOptions...)
	ctx := context.Background()
	var formattedDatabase = fmt.Sprintf("projects/%s/instances/%s/databases/%s", "[PROJECT]", "[INSTANCE]", "[DATABASE]")
	client, err := NewClientWithConfig(ctx, formattedDatabase, config, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return server, client, func() {
		client.Close()
		serverTeardown()
	}
}

// Test validDatabaseName()
func TestValidDatabaseName(t *testing.T) {
	validDbURI := "projects/spanner-cloud-test/instances/foo/databases/foodb"
	invalidDbUris := []string{
		// Completely wrong DB URI.
		"foobarDB",
		// Project ID contains "/".
		"projects/spanner-cloud/test/instances/foo/databases/foodb",
		// No instance ID.
		"projects/spanner-cloud-test/instances//databases/foodb",
	}
	if err := validDatabaseName(validDbURI); err != nil {
		t.Errorf("validateDatabaseName(%q) = %v, want nil", validDbURI, err)
	}
	for _, d := range invalidDbUris {
		if err, wantErr := validDatabaseName(d), "should conform to pattern"; !strings.Contains(err.Error(), wantErr) {
			t.Errorf("validateDatabaseName(%q) = %q, want error pattern %q", validDbURI, err, wantErr)
		}
	}
}

func TestReadOnlyTransactionClose(t *testing.T) {
	// Closing a ReadOnlyTransaction shouldn't panic.
	c := &Client{}
	tx := c.ReadOnlyTransaction()
	tx.Close()
}

func TestClient_Single(t *testing.T) {
	t.Parallel()
	err := testSingleQuery(t, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestClient_Single_Unavailable(t *testing.T) {
	t.Parallel()
	err := testSingleQuery(t, gstatus.Error(codes.Unavailable, "Temporary unavailable"))
	if err != nil {
		t.Fatal(err)
	}
}

func TestClient_Single_InvalidArgument(t *testing.T) {
	t.Parallel()
	err := testSingleQuery(t, gstatus.Error(codes.InvalidArgument, "Invalid argument"))
	if err == nil {
		t.Fatalf("missing expected error")
	} else if gstatus.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
}

func testSingleQuery(t *testing.T, serverError error) error {
	ctx := context.Background()
	server, client, teardown := setupMockedTestServer(t)
	defer teardown()
	if serverError != nil {
		server.TestSpanner.SetError(serverError)
	}
	iter := client.Single().Query(ctx, NewStatement(SelectSingerIDAlbumIDAlbumTitleFromAlbums))
	defer iter.Stop()
	for {
		row, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return err
		}
		var singerID, albumID int64
		var albumTitle string
		if err := row.Columns(&singerID, &albumID, &albumTitle); err != nil {
			return err
		}
	}
	return nil
}

func createSimulatedExecutionTimeWithTwoUnavailableErrors(method string) map[string]SimulatedExecutionTime {
	errors := make([]error, 2)
	errors[0] = gstatus.Error(codes.Unavailable, "Temporary unavailable")
	errors[1] = gstatus.Error(codes.Unavailable, "Temporary unavailable")
	executionTimes := make(map[string]SimulatedExecutionTime)
	executionTimes[method] = SimulatedExecutionTime{
		Errors: errors,
	}
	return executionTimes
}

func TestClient_ReadOnlyTransaction(t *testing.T) {
	t.Parallel()
	if err := testReadOnlyTransaction(t, make(map[string]SimulatedExecutionTime)); err != nil {
		t.Fatal(err)
	}
}

func TestClient_ReadOnlyTransaction_UnavailableOnSessionCreate(t *testing.T) {
	t.Parallel()
	if err := testReadOnlyTransaction(t, createSimulatedExecutionTimeWithTwoUnavailableErrors(MethodCreateSession)); err != nil {
		t.Fatal(err)
	}
}

func TestClient_ReadOnlyTransaction_UnavailableOnBeginTransaction(t *testing.T) {
	t.Parallel()
	if err := testReadOnlyTransaction(t, createSimulatedExecutionTimeWithTwoUnavailableErrors(MethodBeginTransaction)); err != nil {
		t.Fatal(err)
	}
}

func TestClient_ReadOnlyTransaction_UnavailableOnExecuteStreamingSql(t *testing.T) {
	t.Parallel()
	if err := testReadOnlyTransaction(t, createSimulatedExecutionTimeWithTwoUnavailableErrors(MethodExecuteStreamingSql)); err != nil {
		t.Fatal(err)
	}
}

func TestClient_ReadOnlyTransaction_UnavailableOnCreateSessionAndBeginTransaction(t *testing.T) {
	t.Parallel()
	exec := map[string]SimulatedExecutionTime{
		MethodCreateSession:    {Errors: []error{gstatus.Error(codes.Unavailable, "Temporary unavailable")}},
		MethodBeginTransaction: {Errors: []error{gstatus.Error(codes.Unavailable, "Temporary unavailable")}},
	}
	if err := testReadOnlyTransaction(t, exec); err != nil {
		t.Fatal(err)
	}
}

func TestClient_ReadOnlyTransaction_UnavailableOnCreateSessionAndInvalidArgumentOnBeginTransaction(t *testing.T) {
	t.Parallel()
	exec := map[string]SimulatedExecutionTime{
		MethodCreateSession:    {Errors: []error{gstatus.Error(codes.Unavailable, "Temporary unavailable")}},
		MethodBeginTransaction: {Errors: []error{gstatus.Error(codes.InvalidArgument, "Invalid argument")}},
	}
	if err := testReadOnlyTransaction(t, exec); err == nil {
		t.Fatalf("Missing expected exception")
	} else if gstatus.Code(err) != codes.InvalidArgument {
		t.Fatalf("Got unexpected exception: %v", err)
	}
}

func testReadOnlyTransaction(t *testing.T, executionTimes map[string]SimulatedExecutionTime) error {
	server, client, teardown := setupMockedTestServer(t)
	defer teardown()
	for method, exec := range executionTimes {
		server.TestSpanner.PutExecutionTime(method, exec)
	}
	tx := client.ReadOnlyTransaction()
	defer tx.Close()
	ctx := context.Background()
	iter := tx.Query(ctx, NewStatement(SelectSingerIDAlbumIDAlbumTitleFromAlbums))
	defer iter.Stop()
	for {
		row, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return err
		}
		var singerID, albumID int64
		var albumTitle string
		if err := row.Columns(&singerID, &albumID, &albumTitle); err != nil {
			return err
		}
	}
	return nil
}

func TestClient_ReadWriteTransaction(t *testing.T) {
	t.Parallel()
	if err := testReadWriteTransaction(t, make(map[string]SimulatedExecutionTime), 1); err != nil {
		t.Fatal(err)
	}
}

func TestClient_ReadWriteTransactionCommitAborted(t *testing.T) {
	t.Parallel()
	if err := testReadWriteTransaction(t, map[string]SimulatedExecutionTime{
		MethodCommitTransaction: {Errors: []error{gstatus.Error(codes.Aborted, "Transaction aborted")}},
	}, 2); err != nil {
		t.Fatal(err)
	}
}

func TestClient_ReadWriteTransactionExecuteStreamingSqlAborted(t *testing.T) {
	t.Parallel()
	if err := testReadWriteTransaction(t, map[string]SimulatedExecutionTime{
		MethodExecuteStreamingSql: {Errors: []error{gstatus.Error(codes.Aborted, "Transaction aborted")}},
	}, 2); err != nil {
		t.Fatal(err)
	}
}

func TestClient_ReadWriteTransaction_UnavailableOnBeginTransaction(t *testing.T) {
	t.Parallel()
	if err := testReadWriteTransaction(t, map[string]SimulatedExecutionTime{
		MethodBeginTransaction: {Errors: []error{gstatus.Error(codes.Unavailable, "Unavailable")}},
	}, 1); err != nil {
		t.Fatal(err)
	}
}

func TestClient_ReadWriteTransaction_UnavailableOnBeginAndAbortOnCommit(t *testing.T) {
	if err := testReadWriteTransaction(t, map[string]SimulatedExecutionTime{
		MethodBeginTransaction:  {Errors: []error{gstatus.Error(codes.Unavailable, "Unavailable")}},
		MethodCommitTransaction: {Errors: []error{gstatus.Error(codes.Aborted, "Aborted")}},
	}, 2); err != nil {
		t.Fatal(err)
	}
}

func TestClient_ReadWriteTransaction_UnavailableOnExecuteStreamingSql(t *testing.T) {
	t.Parallel()
	if err := testReadWriteTransaction(t, map[string]SimulatedExecutionTime{
		MethodExecuteStreamingSql: {Errors: []error{gstatus.Error(codes.Unavailable, "Unavailable")}},
	}, 1); err != nil {
		t.Fatal(err)
	}
}

func TestClient_ReadWriteTransaction_UnavailableOnBeginAndExecuteStreamingSqlAndTwiceAbortOnCommit(t *testing.T) {
	t.Parallel()
	if err := testReadWriteTransaction(t, map[string]SimulatedExecutionTime{
		MethodBeginTransaction:    {Errors: []error{gstatus.Error(codes.Unavailable, "Unavailable")}},
		MethodExecuteStreamingSql: {Errors: []error{gstatus.Error(codes.Unavailable, "Unavailable")}},
		MethodCommitTransaction:   {Errors: []error{gstatus.Error(codes.Aborted, "Aborted"), gstatus.Error(codes.Aborted, "Aborted")}},
	}, 3); err != nil {
		t.Fatal(err)
	}
}

func TestClient_ReadWriteTransaction_AbortedOnExecuteStreamingSqlAndCommit(t *testing.T) {
	t.Parallel()
	if err := testReadWriteTransaction(t, map[string]SimulatedExecutionTime{
		MethodExecuteStreamingSql: {Errors: []error{gstatus.Error(codes.Aborted, "Aborted")}},
		MethodCommitTransaction:   {Errors: []error{gstatus.Error(codes.Aborted, "Aborted"), gstatus.Error(codes.Aborted, "Aborted")}},
	}, 4); err != nil {
		t.Fatal(err)
	}
}

func TestClient_ReadWriteTransactionCommitAbortedAndUnavailable(t *testing.T) {
	t.Parallel()
	if err := testReadWriteTransaction(t, map[string]SimulatedExecutionTime{
		MethodCommitTransaction: {
			Errors: []error{
				gstatus.Error(codes.Aborted, "Transaction aborted"),
				gstatus.Error(codes.Unavailable, "Unavailable"),
			},
		},
	}, 2); err != nil {
		t.Fatal(err)
	}
}

func TestClient_ReadWriteTransactionCommitAlreadyExists(t *testing.T) {
	t.Parallel()
	if err := testReadWriteTransaction(t, map[string]SimulatedExecutionTime{
		MethodCommitTransaction: {Errors: []error{gstatus.Error(codes.AlreadyExists, "A row with this key already exists")}},
	}, 1); err != nil {
		if gstatus.Code(err) != codes.AlreadyExists {
			t.Fatalf("Got unexpected error %v, expected %v", err, codes.AlreadyExists)
		}
	} else {
		t.Fatalf("Missing expected exception")
	}
}

func testReadWriteTransaction(t *testing.T, executionTimes map[string]SimulatedExecutionTime, expectedAttempts int) error {
	server, client, teardown := setupMockedTestServer(t)
	defer teardown()
	for method, exec := range executionTimes {
		server.TestSpanner.PutExecutionTime(method, exec)
	}
	ctx := context.Background()
	var attempts int
	_, err := client.ReadWriteTransaction(ctx, func(ctx context.Context, tx *ReadWriteTransaction) error {
		attempts++
		iter := tx.Query(ctx, NewStatement(SelectSingerIDAlbumIDAlbumTitleFromAlbums))
		defer iter.Stop()
		for {
			row, err := iter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				return err
			}
			var singerID, albumID int64
			var albumTitle string
			if err := row.Columns(&singerID, &albumID, &albumTitle); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if expectedAttempts != attempts {
		t.Fatalf("unexpected number of attempts: %d, expected %d", attempts, expectedAttempts)
	}
	return nil
}

func TestClient_ApplyAtLeastOnce(t *testing.T) {
	t.Parallel()
	server, client, teardown := setupMockedTestServer(t)
	defer teardown()
	ms := []*Mutation{
		Insert("Accounts", []string{"AccountId", "Nickname", "Balance"}, []interface{}{int64(1), "Foo", int64(50)}),
		Insert("Accounts", []string{"AccountId", "Nickname", "Balance"}, []interface{}{int64(2), "Bar", int64(1)}),
	}
	server.TestSpanner.PutExecutionTime(MethodCommitTransaction,
		SimulatedExecutionTime{
			Errors: []error{gstatus.Error(codes.Aborted, "Transaction aborted")},
		})
	_, err := client.Apply(context.Background(), ms, ApplyAtLeastOnce())
	if err != nil {
		t.Fatal(err)
	}
}

func TestReadWriteTransaction_ErrUnexpectedEOF(t *testing.T) {
	_, client, teardown := setupMockedTestServer(t)
	defer teardown()
	ctx := context.Background()
	var attempts int
	_, err := client.ReadWriteTransaction(ctx, func(ctx context.Context, tx *ReadWriteTransaction) error {
		attempts++
		iter := tx.Query(ctx, NewStatement(SelectSingerIDAlbumIDAlbumTitleFromAlbums))
		defer iter.Stop()
		for {
			row, err := iter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				return err
			}
			var singerID, albumID int64
			var albumTitle string
			if err := row.Columns(&singerID, &albumID, &albumTitle); err != nil {
				return err
			}
		}
		return io.ErrUnexpectedEOF
	})
	if err != io.ErrUnexpectedEOF {
		t.Fatalf("Missing expected error %v, got %v", io.ErrUnexpectedEOF, err)
	}
	if attempts != 1 {
		t.Fatalf("unexpected number of attempts: %d, expected %d", attempts, 1)
	}
}

func TestNewClient_ConnectToEmulator(t *testing.T) {
	ctx := context.Background()

	s, err := benchserver.NewMockCloudSpanner()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	go s.Serve()

	oldEnv := os.Getenv("SPANNER_EMULATOR_HOST")
	os.Setenv("SPANNER_EMULATOR_HOST", s.Addr())
	defer os.Setenv("SPANNER_EMULATOR_HOST", oldEnv)

	c, err := NewClient(ctx, "projects/some-proj/instances/some-inst/databases/some-db")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, err = c.Single().ReadRow(ctx, "Accounts", Key{"alice"}, []string{"balance"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClient_ApiClientHeader(t *testing.T) {
	t.Parallel()
	interceptor := func(
		ctx context.Context,
		method string,
		req interface{},
		reply interface{},
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		// Check that the x-goog-api-client is set.
		headers, ok := metadata.FromOutgoingContext(ctx)
		if !ok {
			return spannerErrorf(codes.Internal, "could not get outgoing metadata")
		}
		token, ok := headers["x-goog-api-client"]
		if !ok {
			return spannerErrorf(codes.Internal, "could not get api client token")
		}
		if token == nil {
			return spannerErrorf(codes.Internal, "nil-value for outgoing api client token")
		}
		if len(token) != 1 {
			return spannerErrorf(codes.Internal, "unexpected number of api client token headers: %v", len(token))
		}
		if !strings.HasPrefix(token[0], "gl-go/") {
			return spannerErrorf(codes.Internal, "unexpected api client token: %v", token[0])
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
	opts := []option.ClientOption{option.WithGRPCDialOption(grpc.WithUnaryInterceptor(interceptor))}
	_, client, teardown := setupMockedTestServerWithConfigAndClientOptions(t, ClientConfig{}, opts)
	defer teardown()
	ctx := context.Background()
	iter := client.Single().Query(ctx, NewStatement(SelectSingerIDAlbumIDAlbumTitleFromAlbums))
	defer iter.Stop()
	for {
		_, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}
