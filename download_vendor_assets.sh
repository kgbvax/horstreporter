#!/bin/bash
mkdir -p static/vendor/js
curl -sLo static/vendor/world.geojson https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_50m_admin_0_countries.geojson
chmod +x download_vendor_assets.sh
echo "Assets downloaded successfully!"
