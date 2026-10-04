#!/bin/bash
mkdir -p static/vendor/js
curl -sLo static/vendor/world.geojson https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_50m_admin_0_countries.geojson
# The app fetches the slimmed copy (fewer properties, 3-decimal coordinates).
node scripts/slim-world-geojson.mjs
chmod +x download_vendor_assets.sh
echo "Assets downloaded successfully!"
