import http from 'k6/http';
import { check, group, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';
import { randomString } from 'https://jslib.k6.io/k6-utils/1.2.0/index.js';
import { htmlReport } from 'https://raw.githubusercontent.com/benc-uk/k6-reporter/main/dist/bundle.js';

// Custom metrics
const checkoutTrend = new Trend('checkout_duration');
const purchaseTrend = new Trend('purchase_duration');
const successRate = new Rate('success_rate');
const checkoutFailRate = new Rate('checkout_fail_rate');
const purchaseFailRate = new Rate('purchase_fail_rate');
const successfulPurchases = new Counter('successful_purchases');

// Default options
export const options = {
    scenarios: {
        checkout_purchase_flow: {
            executor: 'ramping-vus',
            startVUs: 1,
            stages: [
                { duration: '30s', target: 10 },  // Ramp up to 10 users over 30 seconds
                { duration: '1m', target: 10 },   // Stay at 10 users for 1 minute
                { duration: '30s', target: 0 },   // Ramp down to 0 users over 30 seconds
            ],
            gracefulRampDown: '10s',
        },
    },
    thresholds: {
        'http_req_duration': ['p(95)<500'], // 95% of requests should be below 500ms
        'checkout_duration': ['p(95)<300'], // 95% of checkout requests should be below 300ms
        'purchase_duration': ['p(95)<300'], // 95% of purchase requests should be below 300ms
        'success_rate': ['rate>0.95'],      // 95% of requests should be successful
    },
    // Note: Use --quiet CLI flag to reduce console output if needed
    summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)'],
};

// Configuration (can be overridden with environment variables)
const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const START_ITEM_ID = parseInt(__ENV.START_ITEM_ID || '1000');
const MAX_ITEMS_PER_USER = parseInt(__ENV.MAX_ITEMS_PER_USER || '10');
const DEBUG_MODE = __ENV.DEBUG_MODE === 'true' || false;

// Main function executed for each virtual user
export default function () {
    // Generate a unique user ID for this virtual user
    const userId = `k6_user_${randomString(8)}_${__VU}`;

    // Each user will attempt to checkout and purchase multiple items
    for (let i = 0; i < MAX_ITEMS_PER_USER; i++) {
        // Calculate a unique item ID based on VU number and iteration
        const itemId = START_ITEM_ID + ((__VU - 1) * MAX_ITEMS_PER_USER) + i;

        let checkoutCode;

        // Checkout flow
        group('Checkout', function () {
            const checkoutUrl = `${BASE_URL}/checkout?user_id=${userId}&id=${itemId}`;
            const checkoutResponse = http.post(checkoutUrl, null, {
                tags: { name: 'CheckoutRequest' }
            });

            // Use k6's built-in timing metrics instead of manual timing
            // The duration is automatically captured by k6
            checkoutTrend.add(checkoutResponse.timings.duration);

            // Check if checkout was successful
            const checkoutSuccess = check(checkoutResponse, {
                'checkout status is 200': (r) => r.status === 200,
                'checkout has code': (r) => r.json('code') !== undefined,
            });

            if (checkoutSuccess) {
                checkoutCode = checkoutResponse.json('code');
                if (DEBUG_MODE) {
                    console.log(`User ${__VU}: Checkout successful for item ${itemId}. Code: ${checkoutCode} (${checkoutResponse.timings.duration.toFixed(2)}ms)`);
                }
            } else {
                checkoutFailRate.add(1);
                // Always log failures regardless of debug mode
                console.log(`User ${__VU}: Checkout failed for item ${itemId}: ${checkoutResponse.status} - ${checkoutResponse.body}`);
                return; // Skip purchase if checkout failed
            }
        });

        // Only proceed to purchase if checkout was successful
        if (checkoutCode) {
            // Small delay between checkout and purchase to simulate user behavior
            sleep(0.5);

            // Purchase flow
            group('Purchase', function () {
                const purchaseUrl = `${BASE_URL}/purchase?code=${checkoutCode}`;
                const purchaseResponse = http.post(purchaseUrl, null, {
                    tags: { name: 'PurchaseRequest' }
                });

                // Use k6's built-in timing metrics
                purchaseTrend.add(purchaseResponse.timings.duration);

                // Check if purchase was successful
                const purchaseSuccess = check(purchaseResponse, {
                    'purchase status is 200': (r) => r.status === 200,
                    'purchase message is successful': (r) => r.json('message') === 'Purchase successful',
                });

                if (purchaseSuccess) {
                    successRate.add(1);
                    successfulPurchases.add(1);
                    if (DEBUG_MODE) {
                        console.log(`User ${__VU}: Purchase successful for item ${itemId} (${purchaseResponse.timings.duration.toFixed(2)}ms)`);
                    }
                } else {
                    purchaseFailRate.add(1);
                    // Always log failures regardless of debug mode
                    console.log(`User ${__VU}: Purchase failed for item ${itemId}: ${purchaseResponse.status} - ${purchaseResponse.body}`);
                }
            });
        }

        // Add a small random delay between iterations to make the test more realistic
        sleep(Math.random() * 1 + 0.5); // 0.5-1.5 seconds
    }
}

/**
 * Function to handle test setup - runs once at the beginning of the test
 * Verifies that the API is accessible and logs test configuration
 * @returns {Object} Setup data (empty in this case)
 */
export function setup() {
    console.log('=== Performance Test Configuration ===');
    console.log(`Base URL: ${BASE_URL}`);
    console.log(`Items per user: ${MAX_ITEMS_PER_USER}`);
    console.log(`Starting item ID: ${START_ITEM_ID}`);
    console.log(`Debug mode: ${DEBUG_MODE ? 'enabled' : 'disabled'}`);
    console.log('=====================================');

    // Verify that the API is accessible
    const healthCheck = http.get(`${BASE_URL}/health`, {
        tags: { name: 'HealthCheck' }
    });

    if (healthCheck.status !== 200) {
        console.error(`Health check failed: ${healthCheck.status} - ${healthCheck.body}`);
        throw new Error('Health check failed - API is not accessible');
    } else {
        console.log('Health check successful, API is accessible');
    }

    return {};
}

/**
 * Function to handle test summary - runs once at the end of the test
 * Generates HTML and JSON reports with test results
 * @param {Object} data - The summary data object
 * @returns {Object} - Object with report filenames as keys and report contents as values
 */
export function handleSummary(data) {
    console.log('Generating performance test reports...');

    return {
        'summary.html': htmlReport(data),
        'summary.json': JSON.stringify(data, null, 2),
    };
}
