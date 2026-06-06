# LinkFlow — Single-Container Earnings Shortener

Everything ships in **one Docker image**: Go backend + embedded HTML templates + frontend. No nginx, no separate containers.

## Project layout

```
earningshorter/
├── Dockerfile                   ← single build, single image
├── backend/
│   ├── main.go                  ← Go + Gin, uses //go:embed
│   ├── go.mod / go.sum
│   ├── templates/               ← embedded into binary at build time
│   │   ├── step1.html
│   │   ├── step2.html
│   │   └── 404.html
│   └── static/
│       └── index.html           ← frontend, also embedded
└── README.md
```

## Local run

```bash
# Requires MongoDB accessible at MONGO_URI
docker build -t linkflow .
docker run -p 8080:8080 \
  -e MONGO_URI="mongodb://host.docker.internal:27017" \
  -e GIN_MODE=release \
  linkflow
```

Open http://localhost:8080

---

## Deploy on Render

### 1. MongoDB
Create a free cluster on **MongoDB Atlas** → get your connection string:
```
mongodb+srv://user:pass@cluster.mongodb.net/earningshorter
```

### 2. Render Web Service
- **New → Web Service → Docker**
- Point to your repo root (where `Dockerfile` lives)
- Set environment variables:

| Key | Value |
|-----|-------|
| `MONGO_URI` | `mongodb+srv://...` |
| `DB_NAME` | `earningshorter` |
| `GIN_MODE` | `release` |
| `PORT` | `8080` (Render injects this automatically) |

- Health check path: `/health`
- That's it — Render builds and deploys the single image.

---

## Adding ads

Open `backend/templates/step1.html` and `step2.html`.  
Find `<!-- AD UNIT HERE -->` inside `.ad-slot` and drop in your ad network script.  
Rebuild and redeploy.

---

## API reference

| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | Frontend home page |
| POST | `/api/shorten` | `{"url":"https://..."}` → `{"code":"abc1234"}` |
| GET | `/api/info/:code` | Click stats |
| GET | `/:code` | Step 1 interstitial (10s timer) |
| GET | `/:code/step2` | Step 2 interstitial (10s timer) |
| GET | `/:code/go` | Final redirect to original URL |
| GET | `/health` | Health check → `{"status":"ok"}` |
