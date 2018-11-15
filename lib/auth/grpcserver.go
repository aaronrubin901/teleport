/*
Copyright 2018 Gravitational, Inc.

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

package auth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gravitational/teleport"
	"github.com/gravitational/teleport/lib/auth/proto"
	"github.com/gravitational/teleport/lib/services"

	"github.com/gravitational/trace"
	"github.com/gravitational/trace/trail"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/grpclog"
)

func init() {
	w := logrus.StandardLogger().Writer()
	grpclog.SetLoggerV2(grpclog.NewLoggerV2(w, w, w))
}

// GRPCServer is GPRC Auth Server API
type GRPCServer struct {
	*logrus.Entry
	APIConfig
	// httpHandler is a server serving HTTP API
	httpHandler http.Handler
	// grpcHandler is golang GRPC handler
	grpcHandler *grpc.Server
}

// SendKeepAlives allows node to send a stream of keep alive requests
func (g *GRPCServer) SendKeepAlives(AuthService_SendKeepAlivesServer) error {
	return nil
}

// WatchEvents returns a new stream of cluster events
func (g *GRPCServer) WatchEvents(*Watch, AuthService_WatchEventsServer) error {
	return nil
}

// UpsertNode upserts node
func (g *GRPCServer) UpsertNode(context.Context, *Server) (*KeepAlive, error) {
	return nil
}

// UpsertNode upserts node
func (g *GRPCServer) UpsertNode(ctx context.Context, server *proto.Server) (*proto.KeepAlive, error) {
	auth, err := g.authenticate(ctx)
	if err != nil {
		return nil, trail.ToGRPC(err)
	}
	keepAlive, err := auth.UpsertNode()
	if err != nil {
		return nil, trail.ToGRPC(err)
	}
	return &proto.KeepAlive{
		ServerName: keepAlive.ServerName,
		LeaseID:    keepAlive.LeaseID,
	}, nil
}

func (g *GRPCServer) ConnectHeartbeat(stream proto.AuthService_ConnectHeartbeatServer) error {
	clusterName, err := g.AuthServer.GetDomainName()
	if err != nil {
		return trail.ToGRPC(err)
	}

	serverName, err := ExtractHostID(authContext.User.GetName(), clusterName)
	if err != nil {
		log.Warn(err)
		return trail.ToGRPC(trace.AccessDenied("[10] access denied"))
	}

	var serverKind string
	switch {
	case hasBuiltinRole(authContext.Checker, string(teleport.RoleProxy)):
		serverKind = services.KindNode
	case hasBuiltinRole(authContext.Checker, string(teleport.RoleNode)):
		serverKind = services.KindProxy
	default:
		return trail.ToGRPC(trace.AccessDenied("[10] access denied"))
	}
	g.Debugf("Got heartbeat connection from %v %v.", serverKind, serverName)
	for {
		heartbeat, err := stream.Recv()
		if err == io.EOF {
			g.Debugf("Connection closed.")
			return nil
		}
		if err != nil {
			g.Debugf("Failed to receive heartbeat: %v", err)
			return err
		}
		g.Debugf("Lease id: %v", heartbeat.LeaseID)
	}
}

// authServer extracts authentication context and returns initialized auth server
func (g *GRPCServer) authServer(ctx context.Context) (*AuthWithRoles, error) {
	// HTTPS server expects auth  context to be set by the auth middleware
	authContext, err := g.Authorizer.Authorize(ctx)
	if err != nil {
		// propagate connection problem error so we can differentiate
		// between connection failed and access denied
		if trace.IsConnectionProblem(err) {
			return nil, trace.ConnectionProblem(err, "[10] failed to connect to the database")
		} else if trace.IsAccessDenied(err) {
			// don't print stack trace, just log the warning
			log.Warn(err)
		} else {
			log.Warn(trace.DebugReport(err))
		}
		return nil, trace.AccessDenied("[10] access denied")
	}
	return &AuthWithRoles{
		authServer: s.AuthServer,
		user:       authContext.User,
		checker:    authContext.Checker,
		sessions:   s.SessionService,
		alog:       s.AuthServer.IAuditLog,
	}, nil
}

// NewGRPCServer returns a new instance of GRPC server
func NewGRPCServer(cfg APIConfig) http.Handler {
	authServer := &GRPCServer{
		APIConfig: cfg,
		Entry: logrus.WithFields(logrus.Fields{
			trace.Component: teleport.Component(teleport.ComponentAuth, teleport.ComponentGRPC),
		}),
		httpHandler: NewAPIServer(&cfg),
		grpcHandler: grpc.NewServer(),
	}
	proto.RegisterAuthServiceServer(authServer.grpcHandler, authServer)
	return authServer
}

// ServeHTTP dispatches requests based on the request type
func (g *GRPCServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// magic combo match signifying GRPC request
	// https://grpc.io/blog/coreos
	if r.ProtoMajor == 2 && strings.Contains(r.Header.Get("Content-Type"), "application/grpc") {
		g.grpcHandler.ServeHTTP(w, r)
	} else {
		g.httpHandler.ServeHTTP(w, r)
	}
}
