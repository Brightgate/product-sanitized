/*
 * COPYRIGHT 2018 Brightgate Inc.  All rights reserved.
 *
 * This copyright notice is Copyright Management Information under 17 USC 1202
 * and is included to protect this work and deter copyright infringement.
 * Removal or alteration of this Copyright Management Information without the
 * express written permission of Brightgate Inc is prohibited, and any
 * such unauthorized removal or alteration will be a violation of federal law.
 *
 */

package m2mauth

import (
	"context"
	"crypto/rsa"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"bg/base_def"
	"bg/cloud_models/appliancedb"
	"bg/cloud_models/appliancedb/mocks"

	"github.com/grpc-ecosystem/go-grpc-middleware/logging/zap"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	"github.com/dgrijalva/jwt-go"
	"github.com/grpc-ecosystem/go-grpc-middleware/util/metautils"
	"github.com/satori/uuid"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type MockAppliance struct {
	appliancedb.ApplianceID
	ClientID      string
	Prefix        string
	PrivateKeyPEM []byte
	PrivateKey    *rsa.PrivateKey
	PublicKeyPEM  []byte
	Keys          []appliancedb.AppliancePubKey
}

var (
	mockAppliances = []*MockAppliance{
		{
			ApplianceID: appliancedb.ApplianceID{
				CloudUUID: uuid.Must(uuid.FromString("b3798a8e-41e0-4939-a038-e7675af864d5")),
			},
			ClientID: "projects/foo/locations/bar/registries/baz/appliances/mock0",
			Prefix:   "mock0",
		},
		{
			ApplianceID: appliancedb.ApplianceID{
				CloudUUID: uuid.Must(uuid.FromString("099239f6-d8cd-4e57-a696-ef84a3bf39d0")),
			},
			ClientID: "projects/foo/locations/bar/registries/baz/appliances/mock1",
			Prefix:   "mock1",
		},
	}
)

func setupLogging(t *testing.T) (*zap.Logger, *zap.SugaredLogger) {
	// Assign globals
	logger := zaptest.NewLogger(t)
	slogger := logger.Sugar()
	grpc_zap.ReplaceGrpcLogger(logger)
	return logger, slogger
}

func assertErrAndCode(t *testing.T, err error, code codes.Code) {
	if err == nil {
		t.Fatalf("Expected error, but got nil")
	}
	s, ok := status.FromError(err)
	if !ok {
		t.Fatalf("Could not get GRPC status from Error!")
	}
	if s.Code() != code {
		t.Fatalf("Expected code %v, but got %v", code.String(), s.Code().String())
	}
	t.Logf("Saw expected err (code=%s)", code.String())
}

func mbCommon(ma *MockAppliance, token *jwt.Token) string {
	tokenString, err := token.SignedString(ma.PrivateKey)
	if err != nil {
		panic(err)
	}
	return "bearer " + tokenString
}

func makeBearer(ma *MockAppliance) string {
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"exp": int32(time.Now().Unix()) + base_def.BEARER_JWT_EXPIRY_SECS,
	})
	return mbCommon(ma, token)
}

func makeBearerOffset(ma *MockAppliance, offset int32) string {
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"exp": int32(time.Now().Unix()) + offset,
	})
	return mbCommon(ma, token)
}

func makeBearerNoClaims(ma *MockAppliance) string {
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{})
	return mbCommon(ma, token)
}

func makeBearerUnsigned(ma *MockAppliance) string {
	token := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"exp": int32(time.Now().Unix()) + base_def.BEARER_JWT_EXPIRY_SECS,
	})
	tokenString, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		panic(err)
	}
	return "bearer " + tokenString
}

func TestBasic(t *testing.T) {
	_, _ = setupLogging(t)
	m := mockAppliances[0]

	dMock := &mocks.DataStore{}
	dMock.On("ApplianceIDByClientID", mock.Anything, m.ClientID).Return(&m.ApplianceID, nil)
	dMock.On("KeysByUUID", mock.Anything, mock.Anything).Return(m.Keys, nil)
	defer dMock.AssertExpectations(t)

	mw := New(dMock)

	ctx := metautils.ExtractIncoming(context.Background()).
		Add("authorization", makeBearer(m)).
		Add("clientid", m.ClientID).
		ToIncoming(context.Background())
	resultctx, err := mw.authFunc(ctx)
	if err != nil {
		t.Fatalf("saw unexpected error: %+v", err)
	}
	if resultctx == nil {
		t.Fatalf("resultctx is nil")
	}
	if mw.authCache.Len() != 1 {
		t.Fatalf("authCache has unexpected size")
	}
	// try again; we expect this to be served from cache
	resultctx, err = mw.authFunc(ctx)
	if err != nil {
		t.Fatalf("saw unexpected error: %+v", err)
	}
	if resultctx == nil {
		t.Fatalf("resultctx is nil")
	}
}

// TestExpLeeway tests the case where the client is initiating a connection, but
// its clock is slightly ahead of the server, resulting in the Expiry being
// slightly more than BEARER_JWT_EXPIRY_SECS in the "future" (as seen by the
// server).  The code allows a leeway period for this purpose.
func TestExpLeeway(t *testing.T) {
	_, _ = setupLogging(t)
	m := mockAppliances[0]

	dMock := &mocks.DataStore{}
	dMock.On("ApplianceIDByClientID", mock.Anything, m.ClientID).Return(&m.ApplianceID, nil)
	dMock.On("KeysByUUID", mock.Anything, mock.Anything).Return(m.Keys, nil)
	defer dMock.AssertExpectations(t)

	mw := New(dMock)

	ctx := metautils.ExtractIncoming(context.Background()).
		Add("authorization", makeBearerOffset(m, base_def.BEARER_JWT_EXPIRY_SECS+10)).
		Add("clientid", m.ClientID).
		ToIncoming(context.Background())
	resultctx, err := mw.authFunc(ctx)
	if err != nil {
		t.Fatalf("saw unexpected error: %+v", err)
	}
	if resultctx == nil {
		t.Fatalf("resultctx is nil")
	}
}

// TestBadBearer tests a series of cases where the setup/teardown
// is all exactly the same.
func TestBadBearer(t *testing.T) {
	_, _ = setupLogging(t)
	m := mockAppliances[0]

	testCases := []struct {
		desc   string
		bearer string
	}{
		{"BogusBearer", "bearer bogus"},
		// We are mostly protected by our JWT library which doesn't
		// allow unsigned JWTs, but because it is such a substantial
		// threat, we test it anyway.
		// https://auth0.com/blog/critical-vulnerabilities-in-json-web-token-libraries/
		{"UnsignedBearer", makeBearerUnsigned(m)},
		{"ExpClaimExcessive", makeBearerOffset(m, base_def.BEARER_JWT_EXPIRY_SECS*2)},
		{"ExpClaimMissing", makeBearerNoClaims(m)},
		{"ExpClaimExpired", makeBearerOffset(m, -1*base_def.BEARER_JWT_EXPIRY_SECS)},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			dMock := &mocks.DataStore{}
			dMock.On("ApplianceIDByClientID", mock.Anything, m.ClientID).Return(&m.ApplianceID, nil)
			dMock.On("KeysByUUID", mock.Anything, mock.Anything).Return(m.Keys, nil)
			defer dMock.AssertExpectations(t)
			mw := New(dMock)
			ctx := metautils.ExtractIncoming(context.Background()).
				Add("authorization", tc.bearer).
				Add("clientid", m.ClientID).
				ToIncoming(context.Background())
			_, err := mw.authFunc(ctx)
			assertErrAndCode(t, err, codes.Unauthenticated)
			if mw.authCache.Len() != 0 {
				t.Fatalf("authCache has unexpected size")
			}
		})
	}
}

func TestExpiredBearerCached(t *testing.T) {
	_, _ = setupLogging(t)
	m := mockAppliances[0]

	dMock := &mocks.DataStore{}
	defer dMock.AssertExpectations(t)

	mw := New(dMock)

	// Manufacture an expired token, make a bearer for it, then parse the
	// token with claims validation disabled, and stuff the cache it.
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"exp": int32(time.Now().Unix()) - base_def.BEARER_JWT_EXPIRY_SECS,
	})

	tokenString, err := token.SignedString(m.PrivateKey)
	if err != nil {
		panic(err)
	}
	bearer := "bearer " + tokenString

	parser := &jwt.Parser{SkipClaimsValidation: true}
	parsedToken, err := parser.Parse(tokenString, func(*jwt.Token) (interface{}, error) {
		return m.PrivateKey, nil
	})
	_ = mw.authCache.Set(tokenString, &authCacheEntry{
		ClientID:  m.ClientID,
		Token:     parsedToken,
		CloudUUID: m.CloudUUID,
	})
	if mw.authCache.Len() != 1 {
		t.Fatalf("authCache has unexpected size != 1: %v", mw.authCache.Len())
	}

	ctx := metautils.ExtractIncoming(context.Background()).
		Add("authorization", bearer).
		Add("clientid", m.ClientID).
		ToIncoming(context.Background())
	_, err = mw.authFunc(ctx)
	assertErrAndCode(t, err, codes.Unauthenticated)
	if mw.authCache.Len() != 0 {
		t.Fatalf("authCache has unexpected size > 0: %v", mw.authCache.Len())
	}
}

func TestBadClientID(t *testing.T) {
	_, _ = setupLogging(t)
	m := mockAppliances[0]

	dMock := &mocks.DataStore{}
	dMock.On("ApplianceIDByClientID", mock.Anything, mock.Anything).Return(nil, appliancedb.NotFoundError{})
	defer dMock.AssertExpectations(t)

	mw := New(dMock)

	ctx := metautils.ExtractIncoming(context.Background()).
		Add("authorization", makeBearer(m)).
		Add("clientid", m.ClientID+"bad").
		ToIncoming(context.Background())
	_, err := mw.authFunc(ctx)
	assertErrAndCode(t, err, codes.Unauthenticated)
}

func TestCertMismatch(t *testing.T) {
	_, _ = setupLogging(t)
	m := mockAppliances[0]
	m1 := mockAppliances[1]

	dMock := &mocks.DataStore{}
	dMock.On("ApplianceIDByClientID", mock.Anything, m.ClientID).Return(&m.ApplianceID, nil)
	dMock.On("KeysByUUID", mock.Anything, m.CloudUUID).Return(m1.Keys, nil)
	defer dMock.AssertExpectations(t)

	mw := New(dMock)

	ctx := metautils.ExtractIncoming(context.Background()).
		Add("authorization", makeBearer(m)).
		Add("clientid", m.ClientID).
		ToIncoming(context.Background())
	_, err := mw.authFunc(ctx)
	assertErrAndCode(t, err, codes.Unauthenticated)
}

func TestNoKeys(t *testing.T) {
	_, _ = setupLogging(t)
	m := mockAppliances[0]

	dMock := &mocks.DataStore{}
	dMock.On("ApplianceIDByClientID", mock.Anything, m.ClientID).Return(&m.ApplianceID, nil)
	// Return empty keys
	dMock.On("KeysByUUID", mock.Anything, m.CloudUUID).Return([]appliancedb.AppliancePubKey{}, nil)
	defer dMock.AssertExpectations(t)

	mw := New(dMock)

	ctx := metautils.ExtractIncoming(context.Background()).
		Add("authorization", makeBearer(m)).
		Add("clientid", m.ClientID).
		ToIncoming(context.Background())
	_, err := mw.authFunc(ctx)
	assertErrAndCode(t, err, codes.Unauthenticated)
}

func TestEmptyContext(t *testing.T) {
	_, _ = setupLogging(t)

	dMock := &mocks.DataStore{}
	dMock.AssertExpectations(t)

	mw := New(dMock)

	_, err := mw.authFunc(context.Background())
	assertErrAndCode(t, err, codes.Unauthenticated)
}

func loadMock(mock *MockAppliance) {
	var err error
	mock.PrivateKeyPEM, err = ioutil.ReadFile(mock.Prefix + ".rsa_private.pem")
	if err != nil {
		panic(err)
	}
	mock.PrivateKey, err = jwt.ParseRSAPrivateKeyFromPEM(mock.PrivateKeyPEM)
	if err != nil {
		panic(err)
	}
	mock.PublicKeyPEM, err = ioutil.ReadFile(mock.Prefix + ".rsa_cert.pem")
	if err != nil {
		panic(err)
	}
	mock.Keys = []appliancedb.AppliancePubKey{
		{
			ID:     0,
			Format: "RS256_X509",
			Key:    string(mock.PublicKeyPEM),
		},
	}
}

func TestMain(m *testing.M) {
	for _, m := range mockAppliances {
		loadMock(m)
	}
	os.Exit(m.Run())
}
