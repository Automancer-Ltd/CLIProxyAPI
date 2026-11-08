package cliproxy

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/hubauth"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

func (s *Service) startHub(ctx context.Context, cfg *config.Config) error {
	h := cfg.HubAuthority
	if h == nil {
		return nil
	}
	static, err := hubauth.ConfigAuths(cfg)
	if err != nil {
		return err
	}
	if s.coreManager == nil {
		return errors.New("hub authority requires the core manager")
	}
	client, err := hubauth.NewClient(h.URL, h.AccessIDFile, h.AccessSecretFile)
	if err != nil {
		return err
	}
	blobs, err := hubauth.NewB2(h.B2Endpoint, h.B2Bucket, h.B2IDFile, h.B2KeyFile)
	if err != nil {
		return err
	}
	objects, err := hubauth.NewObjects(blobs, h.EncryptionKeyFile, h.Spool)
	if err != nil {
		return err
	}
	coordinator, err := hubauth.NewCoordinator(client, objects, h.Host)
	if err != nil {
		return err
	}
	store := hubauth.NewStore(coordinator)
	if err = store.ConfigureStatic(static); err != nil {
		return err
	}
	s.hubCoordinator = coordinator
	s.hubStore = store
	s.serverOptions = append(s.serverOptions, api.WithMiddleware(s.hubMiddleware()))
	s.coreManager.SetStore(store)
	sdkAuth.RegisterTokenStore(store)
	if s.authManager == nil {
		s.authManager = newDefaultAuthManager()
	}
	s.authManager.SetStore(store)
	// A cold standby may expose its local health/management listener while it
	// awaits ownership. Every credential boundary still refuses until hydration.
	s.syncHub(ctx)
	s.hubDone = make(chan struct{})
	go func() {
		defer close(s.hubDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.syncHub(ctx)
			}
		}
	}()
	return nil
}
func (s *Service) syncHub(ctx context.Context) {
	if err := s.hubCoordinator.Step(ctx); err != nil {
		return
	}
	if err := s.hubCoordinator.Recover(ctx); err != nil {
		log.Debug("hub recovery pending")
	}
	if err := s.coreManager.Load(ctx); err != nil {
		return
	}
	for _, a := range s.coreManager.List() {
		s.ensureExecutorsForAuthWithContext(ctx, a, false)
		s.completeModelRegistrationForAuth(ctx, a)
	}
}

// Shared mode has no auth-file watcher or live config reload. Deny routes that
// could revive those independent state machines, and expose readiness based on
// process ownership plus verified hydration instead of HTTP liveness.
func (s *Service) hubMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if path == "/hub/ready" {
			_, err := s.hubStore.List(c.Request.Context())
			if err != nil {
				c.AbortWithStatusJSON(503, gin.H{"serving": false})
				return
			}
			c.AbortWithStatusJSON(200, gin.H{"serving": true})
			return
		}
		if strings.HasPrefix(path, "/v0/management") || strings.Contains(path, "/plugins") || strings.Contains(path, "/oauth/siwc") || (strings.HasPrefix(path, "/v8/management/config") && c.Request.Method != "GET") {
			c.AbortWithStatusJSON(409, gin.H{"error": "operation is not supported with shared hub authority"})
			return
		}
		// Encrypted CRUD and OAuth login may only be initiated by the current owner.
		if strings.HasPrefix(path, "/v8/management/") {
			if _, err := s.hubStore.List(c.Request.Context()); err != nil {
				c.AbortWithStatusJSON(503, gin.H{"error": "hub authority unavailable"})
				return
			}
		}
		c.Next()
	}
}
