package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

//go:embed templates/*
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

type ShortURL struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Code      string             `bson:"code" json:"code"`
	LongURL   string             `bson:"long_url" json:"long_url"`
	Clicks    int64              `bson:"clicks" json:"clicks"`
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`
}

var (
	client    *mongo.Client
	urlsCol   *mongo.Collection
	tmpl      *template.Template
	hmacSecret []byte
)

// ── Token helpers ─────────────────────────────────────────────────────────────

// makeToken creates a signed token: base64(code:unixTimestamp):hmac
// step = "s2" (step1→step2) or "go" (step2→go)
func makeToken(code, step string) string {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	payload := fmt.Sprintf("%s:%s:%s", code, step, ts)
	mac := hmac.New(sha256.New, hmacSecret)
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	// encode payload so it's URL safe
	encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return encoded + "." + sig
}

// verifyToken validates the token, checks step matches, and that it's within ttl.
func verifyToken(token, code, step string, ttl time.Duration) bool {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	payload := string(payloadBytes)

	// Verify HMAC
	mac := hmac.New(sha256.New, hmacSecret)
	mac.Write([]byte(payload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[1]), []byte(expectedSig)) {
		return false
	}

	// Parse payload: code:step:timestamp
	fields := strings.SplitN(payload, ":", 3)
	if len(fields) != 3 {
		return false
	}
	if fields[0] != code || fields[1] != step {
		return false
	}

	// Check expiry
	ts, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return false
	}
	issued := time.Unix(ts, 0)
	if time.Since(issued) > ttl {
		return false
	}

	return true
}

// ── Util ──────────────────────────────────────────────────────────────────────

func generateCode(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(buf)[:n], nil
}

func getByCode(code string) (*ShortURL, error) {
	var u ShortURL
	err := urlsCol.FindOne(context.Background(), bson.M{"code": code}).Decode(&u)
	return &u, err
}

func incrementClicks(code string) {
	_, _ = urlsCol.UpdateOne(context.Background(),
		bson.M{"code": code},
		bson.M{"$inc": bson.M{"clicks": 1}},
	)
}

func serveIndex(c *gin.Context) {
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		c.String(http.StatusInternalServerError, "index not found")
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", data)
}

func renderTemplate(c *gin.Context, name string, data any) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(c.Writer, name, data); err != nil {
		log.Println("template error:", err)
		c.String(http.StatusInternalServerError, "render error")
	}
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	_ = godotenv.Load()

	mongoURI := os.Getenv("MONGO_URI")
	if mongoURI == "" {
		mongoURI = "mongodb://localhost:27017"
	}
	dbName := os.Getenv("DB_NAME")
	if dbName == "" {
		dbName = "earningshorter"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// HMAC secret — set TOKEN_SECRET env var in production, random fallback for dev
	secret := os.Getenv("TOKEN_SECRET")
	if secret == "" {
		log.Println("WARNING: TOKEN_SECRET not set, using random secret (tokens won't survive restarts)")
		buf := make([]byte, 32)
		_, _ = rand.Read(buf)
		secret = hex.EncodeToString(buf)
	}
	hmacSecret = []byte(secret)

	var err error
	tmpl, err = template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		log.Fatal("template parse error:", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err = mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
	if err != nil {
		log.Fatal("mongo connect:", err)
	}
	if err = client.Ping(ctx, nil); err != nil {
		log.Fatal("mongo ping:", err)
	}
	log.Println("Connected to MongoDB")

	urlsCol = client.Database(dbName).Collection("urls")
	_, _ = urlsCol.Indexes().CreateOne(context.Background(), mongo.IndexModel{
		Keys:    bson.D{{Key: "code", Value: 1}},
		Options: options.Index().SetUnique(true),
	})

	if os.Getenv("GIN_MODE") == "release" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.Default()
	r.Use(cors.New(cors.Config{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{"GET", "POST", "OPTIONS"},
		AllowHeaders: []string{"Content-Type"},
	}))

	r.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })

	r.POST("/api/shorten", shortenHandler)
	r.GET("/api/info/:code", infoHandler)

	r.GET("/:code", page1Handler)
	r.GET("/:code/step2", page2Handler)
	r.GET("/:code/go", goHandler)

	r.GET("/", serveIndex)
	r.NoRoute(func(c *gin.Context) { serveIndex(c) })

	log.Printf("Listening on :%s\n", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatal(err)
	}
}

// ── Route handlers ────────────────────────────────────────────────────────────

func shortenHandler(c *gin.Context) {
	var body struct {
		URL string `json:"url" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "URL is required"})
		return
	}
	ctx := context.Background()
	var existing ShortURL
	if err := urlsCol.FindOne(ctx, bson.M{"long_url": body.URL}).Decode(&existing); err == nil {
		c.JSON(http.StatusOK, gin.H{"code": existing.Code})
		return
	}
	var code string
	for {
		var err error
		code, err = generateCode(7)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "code gen failed"})
			return
		}
		n, _ := urlsCol.CountDocuments(ctx, bson.M{"code": code})
		if n == 0 {
			break
		}
	}
	_, err := urlsCol.InsertOne(ctx, ShortURL{
		ID:        primitive.NewObjectID(),
		Code:      code,
		LongURL:   body.URL,
		Clicks:    0,
		CreatedAt: time.Now(),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": code})
}

func infoHandler(c *gin.Context) {
	var u ShortURL
	if err := urlsCol.FindOne(context.Background(), bson.M{"code": c.Param("code")}).Decode(&u); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": u.Code, "clicks": u.Clicks, "created": u.CreatedAt})
}

// Step 1 — always accessible (it's the entry point).
// Generates a signed token for step2.
func page1Handler(c *gin.Context) {
	code := c.Param("code")
	if code == "favicon.ico" || code == "robots.txt" {
		c.Status(404)
		return
	}
	u, err := getByCode(code)
	if err != nil {
		renderTemplate(c, "404.html", nil)
		return
	}
	incrementClicks(code)

	// Issue token that unlocks step2
	token := makeToken(code, "s2")
	renderTemplate(c, "step1.html", gin.H{
		"code":    u.Code,
		"nextURL": fmt.Sprintf("/%s/step2?t=%s", u.Code, token),
	})
}

// Step 2 — only reachable with a valid token issued by step1.
// Generates a new token for /go.
func page2Handler(c *gin.Context) {
	code := c.Param("code")
	token := c.Query("t")

	// Validate token from step1 (15 min TTL)
	if !verifyToken(token, code, "s2", 15*time.Minute) {
		// Invalid or missing token → send back to step1
		c.Redirect(http.StatusFound, "/"+code)
		return
	}

	if _, err := getByCode(code); err != nil {
		renderTemplate(c, "404.html", nil)
		return
	}

	// Issue a new token that unlocks /go
	goToken := makeToken(code, "go")
	renderTemplate(c, "step2.html", gin.H{
		"code":  code,
		"goURL": fmt.Sprintf("/%s/go?t=%s", code, goToken),
	})
}

// Final redirect — only works with a valid token issued by step2.
func goHandler(c *gin.Context) {
	code := c.Param("code")
	token := c.Query("t")

	// Validate token from step2 (15 min TTL)
	if !verifyToken(token, code, "go", 15*time.Minute) {
		// Invalid or missing token → send back to step1
		c.Redirect(http.StatusFound, "/"+code)
		return
	}

	u, err := getByCode(code)
	if err != nil {
		renderTemplate(c, "404.html", nil)
		return
	}
	c.Redirect(http.StatusTemporaryRedirect, u.LongURL)
}
