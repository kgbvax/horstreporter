# HorstReporter

HorstReporter is a web application designed to provide a real-time visualization of HF radio propagation based on reception reports from the PSK Reporter network. It helps amateur radio operators quickly assess current band conditions.

## Core Functionality

The application consists of a Go backend and a JavaScript frontend.

### Backend (main.go)

-   **Data Streaming**: The backend connects to the public MQTT feed of `pskreporter.info`.
-   **Filtering**: It filters the global stream of reception reports (spots) based on a user-provided callsign or Maidenhead locator. It captures spots where the user is either the sending or receiving station.
-   **SSE API**: It processes the filtered spots and streams them to the frontend in real-time using a Server-Sent Events (SSE) endpoint (`/api/stream`). Only reports for FT8 and FT4 modes are considered.
-   **Web Server**: It serves the static frontend files (HTML, CSS, JS).

### Frontend (app.js)

-   **Interactive Map**: Uses Leaflet.js to display a world map.
-   **User Input**: The user can specify a callsign or locator to monitor, and a time window (e.g., last 15 minutes) for the data.
-   **Heatmap Visualization**:
    -   Instead of individual spots, the map displays data aggregated into Maidenhead grid squares (e.g., `JO62`).
    -   The **color** of a grid square indicates the **dominant amateur radio band** (the one with the most activity) within that square.
    -   The **opacity** of the square represents the average Signal-to-Noise Ratio (SNR), creating a "hotness" effect: stronger signals result in more opaque squares. This helps in identifying areas where a connection is likely viable.
-   **Dynamic Updates**: The map updates in real-time as new spots arrive. Old spots are automatically removed from the dataset after the configured time window expires.

## Features

-   **Live Mode**: Connects to the stream and displays data as it happens.
-   **SNR Filter**: An option to hide reports with an SNR of 0 dB or less, focusing on stronger signals.
-   **Band Filter**: Users can select to view all bands or filter for a specific band of interest. Active bands are highlighted in the UI.
-   **Band Cycler**: An automatic function to cycle through the currently active bands, providing a dynamic overview of conditions.
-   **Geolocation**: A button to automatically detect the user's locator via the browser's geolocation API.
-   **Tooltip Details**: Hovering over a grid square on the map shows a tooltip with detailed statistics: min/max/avg SNR, the best band, and the total number of spots.

