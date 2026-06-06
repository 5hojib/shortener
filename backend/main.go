package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"html/template"
	"log"
	"net/http"
	"os"
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
	client  *mongo.Client
	urlsCol *mongo.Collection
	tmpl    *template.Template
)

func generateCode(n int) (string, error) {
	buf := make([]byte, n)
	_, err := rand.Read(buf)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(buf)[:n], nil
}

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

	// Parse embedded templates
	var err error
	tmpl, err = template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		log.Fatal("Template parse error:", err)
	}

	// Connect MongoDB
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err = mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
	if err != nil {
		log.Fatal("MongoDB connect error:", err)
	}
	if err = client.Ping(ctx, nil); err != nil {
		log.Fatal("MongoDB ping error:", err)
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

	// Health
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// API
	r.POST("/api/shorten", shortenHandler)
	r.GET("/api/info/:code", infoHandler)

	// Interstitial pages
	r.GET("/:code", page1Handler)
	r.GET("/:code/step2", page2Handler)
	r.GET("/:code/go", goHandler)

	// Frontend — serve index.html from embedded static/
	r.GET("/", serveIndex)
	r.NoRoute(func(c *gin.Context) {
		// Serve index for unknown paths so SPA works
		serveIndex(c)
	})

	log.Printf("Listening on :%s\n", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatal(err)
	}
}

func serveIndex(c *gin.Context) {
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		c.String(http.StatusInternalServerError, "index.html not found")
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

// ── Handlers ──────────────────────────────────────────────

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

func page1Handler(c *gin.Context) {
	code := c.Param("code")
	// Skip known paths that are routed to NoRoute → index
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
	renderTemplate(c, "step1.html", gin.H{
		"code":    u.Code,
		"nextURL": "/" + u.Code + "/step2",
	})
}

func page2Handler(c *gin.Context) {
	code := c.Param("code")
	if _, err := getByCode(code); err != nil {
		renderTemplate(c, "404.html", nil)
		return
	}
	renderTemplate(c, "step2.html", gin.H{
		"code":  code,
		"goURL": "/" + code + "/go",
	})
}

func goHandler(c *gin.Context) {
	code := c.Param("code")
	u, err := getByCode(code)
	if err != nil {
		renderTemplate(c, "404.html", nil)
		return
	}
	c.Redirect(http.StatusTemporaryRedirect, u.LongURL)
}
