# Performance Testing (with k6)

This directory contains performance test scripts for the Not-Sale-Back application. The tests are designed to simulate real-world usage patterns and measure the application's performance under load.

## Important
10k items is not that much to test the load in a good way, at some points it's all sold :)

## Reports
Sample (developer run) reports can be found at:
- [HTML Report](html-report.html)
- [Summary](summary.html)

## Prerequisites

To run the performance tests, you need to have [k6](https://k6.io/) installed on your system.

### Installing k6

#### macOS
```bash
brew install k6
```

#### Linux
```bash
sudo apt-key adv --keyserver hkp://keyserver.ubuntu.com:80 --recv-keys C5AD17C747E3415A3642D57D77C6C491D6AC1D69
echo "deb https://dl.k6.io/deb stable main" | sudo tee /etc/apt/sources.list.d/k6.list
sudo apt-get update
sudo apt-get install k6
```

#### Windows
```bash
choco install k6
```

### perf.js

This script tests the checkout and purchase flow of the application. It simulates multiple users making checkout requests and then using the returned code to make purchase requests.

#### Key Features:
- Simulates realistic user behavior with random delays
- Measures checkout and purchase durations separately
- Generates detailed HTML and text reports
- Configurable via environment variables
- Includes thresholds for acceptable performance

## Running the Tests

### Basic Usage

To run the k6 performance test with default settings:

```bash
K6_WEB_DASHBOARD=true K6_WEB_DASHBOARD_EXPORT=html-report.html k6 run perf.js
```

### Configuration Options

You can customize the test behavior using environment variables:

```bash
# Set the base URL of the application
BASE_URL=http://localhost:8080 \
# Set the starting item ID
START_ITEM_ID=1000 \
# Set the maximum number of items per user
MAX_ITEMS_PER_USER=5 \
K6_WEB_DASHBOARD=true K6_WEB_DASHBOARD_EXPORT=html-report.html k6 run dev/perf.js
```

## Understanding Test Results

After running the test, k6 will output a summary of the results to the console. Additionally, the script generates an HTML report (`summary.html`) with detailed metrics.

### Key Metrics

- **Success Rate**: Percentage of successful purchase requests
- **Checkout Duration**: Time taken to complete checkout requests
- **Purchase Duration**: Time taken to complete purchase requests
- **HTTP Request Duration**: Overall HTTP request duration
- **Throughput**: Number of successful requests per second

### Thresholds

The script defines the following thresholds for acceptable performance:

- 95% of HTTP requests should complete in less than 500ms
- 95% of checkout requests should complete in less than 300ms
- 95% of purchase requests should complete in less than 300ms
- At least 95% of requests should be successful
