/*
 * COPYRIGHT 2019 Brightgate Inc.  All rights reserved.
 *
 * This copyright notice is Copyright Management Information under 17 USC 1202
 * and is included to protect this work and deter copyright infringement.
 * Removal or alteration of this Copyright Management Information without the
 * express written permission of Brightgate Inc is prohibited, and any
 * such unauthorized removal or alteration will be a violation of federal law.
 *
 */

package main

import (
	"context"
	"fmt"
	"time"

	"bg/cl_common/daemonutils"
	"bg/cloud_models/appliancedb"
	"bg/cloud_rpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type certServer struct {
	applianceDB appliancedb.DataStore
}

func newCertServer(applianceDB appliancedb.DataStore) *certServer {
	return &certServer{
		applianceDB: applianceDB,
	}
}

func errToCode(err error) codes.Code {
	var code codes.Code
	switch err.(type) {
	case appliancedb.NotFoundError:
		code = codes.NotFound
	case nil:
		code = codes.OK
	default:
		code = codes.Internal
	}
	return code
}

func (cs *certServer) Download(ctx context.Context, req *cloud_rpc.CertificateRequest) (*cloud_rpc.CertificateResponse, error) {
	_, slog := daemonutils.EndpointLogger(ctx)

	siteUU, err := getSiteUUID(ctx, false)
	if err != nil {
		slog.Errorw("Failed to process certificate retrieval",
			"error", err)
		return nil, err
	}

	jurisdiction := "" // XXX Need lookup
	domain, isNew, err := cs.applianceDB.RegisterDomain(ctx, siteUU, jurisdiction)
	if err != nil {
		verb := map[bool]string{true: "register", false: "determine"}
		msg := fmt.Sprintf("Failed to %s domain", verb[isNew])
		slog.Errorw(msg, "error", err)
		return nil, status.Errorf(codes.Internal, "%s: %v", msg, err)
	}
	if isNew {
		slog.Infow("Claimed domain for site", "domain", domain)
	}

	slog.Info("Processing certificate retrieval")
	certInfo, err := cs.applianceDB.ServerCertByUUID(ctx, siteUU)
	if err != nil {
		slog.Errorw("Failed to find server certificate", "error", err)
		return nil, status.Errorf(errToCode(err),
			"Failed to find server certificate: %v", err)
	}
	if certInfo.Expiration.Before(time.Now()) {
		expired := time.Now().Sub(certInfo.Expiration)
		slog.Errorw("Found already-expired certificate",
			"expired", expired)
		return nil, status.Errorf(codes.Internal,
			"Found already-expired certificate")
	}

	return &cloud_rpc.CertificateResponse{
		Fingerprint: certInfo.Fingerprint,
		Certificate: certInfo.Cert,
		IssuerCert:  certInfo.IssuerCert,
		Key:         certInfo.Key,
	}, nil
}
