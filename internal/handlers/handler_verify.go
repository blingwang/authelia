package handlers

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/valyala/fasthttp"

	"github.com/authelia/authelia/internal/authentication"
	"github.com/authelia/authelia/internal/authorization"
	"github.com/authelia/authelia/internal/middlewares"
)

func isURLUnderProtectedDomain(url *url.URL, domain string) bool {
	return strings.HasSuffix(url.Hostname(), domain)
}

func isSchemeHTTPS(url *url.URL) bool {
	return url.Scheme == "https"
}

func isSchemeWSS(url *url.URL) bool {
	return url.Scheme == "wss"
}

// getOriginalURL extract the URL from the request headers (X-Original-URI or X-Forwarded-* headers)
func getOriginalURL(ctx *middlewares.AutheliaCtx) (*url.URL, error) {
	originalURL := ctx.XOriginalURL()
	if originalURL != nil {
		url, err := url.ParseRequestURI(string(originalURL))
		if err != nil {
			return nil, fmt.Errorf("Unable to parse URL extracted from X-Original-URL header: %v", err)
		}
		ctx.Logger.Debug("Using X-Original-URL header content as targeted site URL")
		return url, nil
	}

	forwardedProto := ctx.XForwardedProto()
	forwardedHost := ctx.XForwardedHost()
	forwardedURI := ctx.XForwardedURI()

	if forwardedProto == nil {
		return nil, errMissingXForwardedProto
	}

	if forwardedHost == nil {
		return nil, errMissingXForwardedHost
	}

	var requestURI string
	scheme := append(forwardedProto, protoHostSeparator...)
	requestURI = string(append(scheme,
		append(forwardedHost, forwardedURI...)...))

	url, err := url.ParseRequestURI(requestURI)
	if err != nil {
		return nil, fmt.Errorf("Unable to parse URL %s: %v", requestURI, err)
	}
	ctx.Logger.Debugf("Using X-Fowarded-Proto, X-Forwarded-Host and X-Forwarded-URI headers " +
		"to construct targeted site URL")
	return url, nil
}

// parseBasicAuth parses an HTTP Basic Authentication string
// "Basic QWxhZGRpbjpvcGVuIHNlc2FtZQ==" returns ("Aladdin", "open sesame", true)
func parseBasicAuth(auth string) (username, password string, err error) {
	if !strings.HasPrefix(auth, authPrefix) {
		return "", "", fmt.Errorf("%s prefix not found in %s header", strings.Trim(authPrefix, " "), AuthorizationHeader)
	}
	c, err := base64.StdEncoding.DecodeString(auth[len(authPrefix):])
	if err != nil {
		return "", "", err
	}
	cs := string(c)
	s := strings.IndexByte(cs, ':')
	if s < 0 {
		return "", "", fmt.Errorf("Format of %s header must be user:password", AuthorizationHeader)
	}
	return cs[:s], cs[s+1:], nil
}

// isTargetURLAuthorized check whether the given user is authorized to access the resource
func isTargetURLAuthorized(authorizer *authorization.Authorizer, targetURL url.URL,
	username string, userGroups []string, clientIP net.IP, authLevel authentication.Level) authorizationMatching {
	level := authorizer.GetRequiredLevel(authorization.Subject{
		Username: username,
		Groups:   userGroups,
		IP:       clientIP,
	}, targetURL)

	if level == authorization.Bypass {
		return Authorized
	} else if username != "" && level == authorization.Denied {
		// If the user is not anonymous, it means that we went through
		// all the rules related to that user and knowing who he is we can
		// deduce the access is forbidden
		// For anonymous users though, we cannot be sure that she
		// could not be granted the rights to access the resource. Consequently
		// for anonymous users we send Unauthorized instead of Forbidden
		return Forbidden
	} else {
		if level == authorization.OneFactor &&
			authLevel >= authentication.OneFactor {
			return Authorized
		} else if level == authorization.TwoFactor &&
			authLevel >= authentication.TwoFactor {
			return Authorized
		}
	}
	return NotAuthorized
}

// verifyBasicAuth verify that the provided username and password are correct and
// that the user is authorized to target the resource
func verifyBasicAuth(auth []byte, targetURL url.URL, ctx *middlewares.AutheliaCtx) (username string, groups []string, authLevel authentication.Level, err error) { //nolint:unparam
	username, password, err := parseBasicAuth(string(auth))

	if err != nil {
		return "", nil, authentication.NotAuthenticated, fmt.Errorf("Unable to parse content of %s header: %s", AuthorizationHeader, err)
	}

	authenticated, err := ctx.Providers.UserProvider.CheckUserPassword(username, password)

	if err != nil {
		return "", nil, authentication.NotAuthenticated, fmt.Errorf("Unable to check credentials extracted from %s header: %s", AuthorizationHeader, err)
	}

	// If the user is not correctly authenticated, send a 401
	if !authenticated {
		// Request Basic Authentication otherwise
		return "", nil, authentication.NotAuthenticated, fmt.Errorf("User %s is not authenticated", username)
	}

	details, err := ctx.Providers.UserProvider.GetDetails(username)

	if err != nil {
		return "", nil, authentication.NotAuthenticated, fmt.Errorf("Unable to retrieve details of user %s: %s", username, err)
	}

	return username, details.Groups, authentication.OneFactor, nil
}

// setForwardedHeaders set the forwarded User and Groups headers
func setForwardedHeaders(headers *fasthttp.ResponseHeader, username string, groups []string) {
	if username != "" {
		headers.Set(remoteUserHeader, username)
		headers.Set(remoteGroupsHeader, strings.Join(groups, ","))
	}
}

// hasUserBeenInactiveLongEnough check whether the user has been inactive for too long
func hasUserBeenInactiveLongEnough(ctx *middlewares.AutheliaCtx) (bool, error) { //nolint:unparam
	maxInactivityPeriod := int64(ctx.Providers.SessionProvider.Inactivity.Seconds())
	if maxInactivityPeriod == 0 {
		return false, nil
	}

	lastActivity := ctx.GetSession().LastActivity
	inactivityPeriod := ctx.Clock.Now().Unix() - lastActivity

	ctx.Logger.Tracef("Inactivity report: Inactivity=%d, MaxInactivity=%d",
		inactivityPeriod, maxInactivityPeriod)

	if inactivityPeriod > maxInactivityPeriod {
		return true, nil
	}

	return false, nil
}

// verifyFromSessionCookie verify if a user identified by a cookie is allowed to access target URL
func verifyFromSessionCookie(targetURL url.URL, ctx *middlewares.AutheliaCtx) (username string, groups []string, authLevel authentication.Level, err error) { //nolint:unparam
	userSession := ctx.GetSession()
	// No username in the session means the user is anonymous
	isUserAnonymous := userSession.Username == ""

	if isUserAnonymous && userSession.AuthenticationLevel != authentication.NotAuthenticated {
		return "", nil, authentication.NotAuthenticated, fmt.Errorf("An anonymous user cannot be authenticated. That might be the sign of a compromise")
	}

	if !userSession.KeepMeLoggedIn && !isUserAnonymous {
		inactiveLongEnough, err := hasUserBeenInactiveLongEnough(ctx)
		if err != nil {
			return "", nil, authentication.NotAuthenticated, fmt.Errorf("Unable to check if user has been inactive for a long time: %s", err)
		}

		if inactiveLongEnough {
			// Destroy the session a new one will be regenerated on next request
			err := ctx.Providers.SessionProvider.DestroySession(ctx.RequestCtx)
			if err != nil {
				return "", nil, authentication.NotAuthenticated, fmt.Errorf("Unable to destroy user session after long inactivity: %s", err)
			}

			return userSession.Username, userSession.Groups, authentication.NotAuthenticated, fmt.Errorf("User %s has been inactive for too long", userSession.Username)
		}
	}
	return userSession.Username, userSession.Groups, userSession.AuthenticationLevel, nil
}

func handleUnauthorized(ctx *middlewares.AutheliaCtx, targetURL fmt.Stringer, username string) {
	// Kubernetes ingress controller and Traefik use the rd parameter of the verify
	// endpoint to provide the URL of the login portal. The target URL of the user
	// is computed from X-Fowarded-* headers or X-Original-URL
	rd := string(ctx.QueryArgs().Peek("rd"))
	if rd != "" {
		redirectionURL := fmt.Sprintf("%s?rd=%s", rd, url.QueryEscape(targetURL.String()))
		if strings.Contains(redirectionURL, "/%23/") {
			ctx.Logger.Warn("Characters /%23/ have been detected in redirection URL. This is not needed anymore, please strip it")
		}
		ctx.Logger.Infof("Access to %s is not authorized to user %s, redirecting to %s", targetURL.String(), username, redirectionURL)
		ctx.Redirect(redirectionURL, 302)
		ctx.SetBodyString(fmt.Sprintf("Found. Redirecting to %s", redirectionURL))
	} else {
		ctx.Logger.Infof("Access to %s is not authorized to user %s, sending 401 response", targetURL.String(), username)
		ctx.ReplyUnauthorized()
	}
}

func updateActivityTimestamp(ctx *middlewares.AutheliaCtx, isBasicAuth bool, username string) error {
	if isBasicAuth || username == "" {
		return nil
	}

	userSession := ctx.GetSession()
	// We don't need to update the activity timestamp when user checked keep me logged in.
	if userSession.KeepMeLoggedIn {
		return nil
	}

	// Mark current activity
	userSession.LastActivity = ctx.Clock.Now().Unix()
	return ctx.SaveSession(userSession)
}

// VerifyGet is the handler verifying if a request is allowed to go through
func VerifyGet(ctx *middlewares.AutheliaCtx) {
	ctx.Logger.Tracef("Headers=%s", ctx.Request.Header.String())
	targetURL, err := getOriginalURL(ctx)

	if err != nil {
		ctx.Error(fmt.Errorf("Unable to parse target URL: %s", err), operationFailedMessage)
		return
	}

	if !isSchemeHTTPS(targetURL) && !isSchemeWSS(targetURL) {
		ctx.Logger.Error(fmt.Errorf("Scheme of target URL %s must be secure since cookies are "+
			"only transported over a secure connection for security reasons", targetURL.String()))
		ctx.ReplyUnauthorized()
		return
	}

	if !isURLUnderProtectedDomain(targetURL, ctx.Configuration.Session.Domain) {
		ctx.Logger.Error(fmt.Errorf("The target URL %s is not under the protected domain %s",
			targetURL.String(), ctx.Configuration.Session.Domain))
		ctx.ReplyUnauthorized()
		return
	}

	var username string
	var groups []string
	var authLevel authentication.Level

	proxyAuthorization := ctx.Request.Header.Peek(AuthorizationHeader)
	isBasicAuth := proxyAuthorization != nil

	if isBasicAuth {
		username, groups, authLevel, err = verifyBasicAuth(proxyAuthorization, *targetURL, ctx)
	} else {
		username, groups, authLevel, err = verifyFromSessionCookie(*targetURL, ctx)
	}

	if err != nil {
		ctx.Logger.Error(fmt.Sprintf("Error caught when verifying user authorization: %s", err))
		if err := updateActivityTimestamp(ctx, isBasicAuth, username); err != nil {
			ctx.Error(fmt.Errorf("Unable to update last activity: %s", err), operationFailedMessage)
			return
		}
		handleUnauthorized(ctx, targetURL, username)
		return
	}

	authorization := isTargetURLAuthorized(ctx.Providers.Authorizer, *targetURL, username,
		groups, ctx.RemoteIP(), authLevel)

	if authorization == Forbidden {
		ctx.Logger.Infof("Access to %s is forbidden to user %s", targetURL.String(), username)
		ctx.ReplyForbidden()
	} else if authorization == NotAuthorized {
		handleUnauthorized(ctx, targetURL, username)
	} else if authorization == Authorized {
		setForwardedHeaders(&ctx.Response.Header, username, groups)
	}

	if err := updateActivityTimestamp(ctx, isBasicAuth, username); err != nil {
		ctx.Error(fmt.Errorf("Unable to update last activity: %s", err), operationFailedMessage)
	}
}
