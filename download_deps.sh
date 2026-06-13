#!/bin/bash
echo "Downloading frontend dependencies..."

mkdir -p static/vendor/css/images
mkdir -p static/vendor/js
mkdir -p static/vendor/webfonts

# Bootstrap
curl -sL https://cdn.jsdelivr.net/npm/bootstrap@5.3.2/dist/css/bootstrap.min.css -o static/vendor/css/bootstrap.min.css
curl -sL https://cdn.jsdelivr.net/npm/bootstrap@5.3.2/dist/js/bootstrap.bundle.min.js -o static/vendor/js/bootstrap.bundle.min.js

# Leaflet (CSS, JS, and marker images)
curl -sL https://unpkg.com/leaflet/dist/leaflet.css -o static/vendor/css/leaflet.css
curl -sL https://unpkg.com/leaflet/dist/leaflet.js -o static/vendor/js/leaflet.js
curl -sL https://unpkg.com/leaflet/dist/images/marker-icon.png -o static/vendor/css/images/marker-icon.png
curl -sL https://unpkg.com/leaflet/dist/images/marker-icon-2x.png -o static/vendor/css/images/marker-icon-2x.png
curl -sL https://unpkg.com/leaflet/dist/images/marker-shadow.png -o static/vendor/css/images/marker-shadow.png

# Turf
curl -sL https://cdn.jsdelivr.net/npm/@turf/turf@6.5.0/turf.min.js -o static/vendor/js/turf.min.js

# Marked
curl -sL https://cdn.jsdelivr.net/npm/marked/marked.min.js -o static/vendor/js/marked.min.js

# Font Awesome (CSS and Webfonts)
curl -sL https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.4.0/css/all.min.css -o static/vendor/css/all.min.css
for font in fa-solid-900 fa-regular-400 fa-brands-400 fa-v4compatibility; do
    curl -sL "https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.4.0/webfonts/${font}.woff2" -o "static/vendor/webfonts/${font}.woff2"
done

echo "All dependencies downloaded successfully!"