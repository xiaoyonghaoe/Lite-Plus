package subhttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSharePageShowsTrafficAndNodesWithProtocol(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, row := subscriptionFixture(t)
	user, err := st.GetUser(row.UserID)
	require.NoError(t, err)
	user.UsedBytes = 2 * 1024 * 1024 * 1024
	user.QuotaBytes = 10 * 1024 * 1024 * 1024
	require.NoError(t, st.UpsertUser(user))

	// Explicit ?view=share returns HTML share page with traffic and node protocol.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "https://panel.example.com/sub/"+row.Token+"?view=share", nil)
	Write(c, st, row.Token)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Header().Get("Content-Type"), "text/html")
	body := w.Body.String()
	require.Contains(t, body, "2.00 GiB")
	require.Contains(t, body, "10.00 GiB")
	require.Contains(t, body, "8.00 GiB")
	require.Contains(t, body, "allowed")
	require.Contains(t, body, "Trojan")
	require.NotContains(t, body, "forbidden.example.com")

	// Browser Accept: text/html without client UA also renders share page.
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodGet, "https://panel.example.com/sub/"+row.Token, nil)
	c2.Request.Header.Set("User-Agent", "Mozilla/5.0 Chrome/130.0")
	c2.Request.Header.Set("Accept", "text/html,application/xhtml+xml")
	Write(c2, st, row.Token)
	require.Equal(t, http.StatusOK, w2.Code)
	require.Contains(t, w2.Header().Get("Content-Type"), "text/html")

	// Proxy client UA gets subscription config even if Accept includes text/html.
	w3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(w3)
	c3.Request = httptest.NewRequest(http.MethodGet, "https://panel.example.com/sub/"+row.Token, nil)
	c3.Request.Header.Set("User-Agent", "ClashMeta/1.19")
	c3.Request.Header.Set("Accept", "text/html,*/*")
	Write(c3, st, row.Token)
	require.Equal(t, http.StatusOK, w3.Code)
	require.Equal(t, "mihomo", w3.Header().Get("X-Subscription-Client"))
	require.NotContains(t, w3.Header().Get("Content-Type"), "text/html")

	// ?raw=1 forces raw subscription content even in a browser.
	w4 := httptest.NewRecorder()
	c4, _ := gin.CreateTestContext(w4)
	c4.Request = httptest.NewRequest(http.MethodGet, "https://panel.example.com/sub/"+row.Token+"?raw=1", nil)
	c4.Request.Header.Set("User-Agent", "Mozilla/5.0 Chrome/130.0")
	c4.Request.Header.Set("Accept", "text/html")
	Write(c4, st, row.Token)
	require.Equal(t, http.StatusOK, w4.Code)
	require.NotContains(t, w4.Header().Get("Content-Type"), "text/html")
}
