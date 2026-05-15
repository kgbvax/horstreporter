#!/bin/bash
mkdir -p static/vendor/js
curl -sLo static/vendor/js/proj4.js https://cdnjs.cloudflare.com/ajax/libs/proj4js/2.9.0/proj4.js
curl -sLo static/vendor/js/proj4leaflet.js https://cdnjs.cloudflare.com/ajax/libs/proj4leaflet/1.0.2/proj4leaflet.js
curl -sLo static/vendor/world.geojson https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_50m_admin_0_countries.geojson
chmod +x download_vendor_assets.sh
echo "Assets downloaded successfully!"
