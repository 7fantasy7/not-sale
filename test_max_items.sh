#!/bin/bash

# AI Generated

# Script to test the maximum items per user limit
# This script will:
# 1. Run 10 successful checkout and purchase operations for a single user (items START_ITEM_ID to START_ITEM_ID+9)
# 2. Successfully checkout an 11th item (item START_ITEM_ID+10)
# 3. Attempt to purchase the 11th item, which should fail with a "User has reached the maximum of 10 items per sale" error
#
# Usage: ./test_max_items.sh [start_item_id]
# If start_item_id is not provided, it defaults to 1

# Set the starting item ID (default to 1 if not provided)
START_ITEM_ID=${1:-100}
echo "Using starting item ID: $START_ITEM_ID"

# Set the user ID
USER_ID="test_user_$(date +%s)"
echo "Using user ID: $USER_ID"

# Function to extract the code from the checkout response
extract_code() {
    echo "$1" | grep -o '"code":"[^"]*"' | cut -d'"' -f4
}

# Function to check if the checkout was successful
is_checkout_successful() {
    echo "$1" | grep -q '"code":'
}

# Function to check if the purchase was successful
is_purchase_successful() {
    echo "$1" | grep -q '"message":"Purchase successful"'
}

# Run 10 successful checkout and purchase operations
for i in {0..9}; do
    ITEM_ID=$((START_ITEM_ID + i))
    echo "Attempt $((i+1)): Checking out item $ITEM_ID..."
    CHECKOUT_RESPONSE=$(curl -s -X POST "http://localhost:8080/checkout?user_id=$USER_ID&id=$ITEM_ID")

    if is_checkout_successful "$CHECKOUT_RESPONSE"; then
        CODE=$(extract_code "$CHECKOUT_RESPONSE")
        echo "Checkout successful. Code: $CODE"

        echo "Purchasing item $i..."
        PURCHASE_RESPONSE=$(curl -s -X POST "http://localhost:8080/purchase?code=$CODE")

        if is_purchase_successful "$PURCHASE_RESPONSE"; then
            echo "Purchase successful."
        else
            echo "Purchase failed: $PURCHASE_RESPONSE"
            exit 1
        fi
    else
        echo "Checkout failed: $CHECKOUT_RESPONSE"
        exit 1
    fi
done

# Attempt an 11th checkout, which should succeed
LAST_ITEM_ID=$((START_ITEM_ID + 10))
echo "Attempting 11th checkout (should succeed) for item $LAST_ITEM_ID..."
CHECKOUT_RESPONSE=$(curl -s -X POST "http://localhost:8080/checkout?user_id=$USER_ID&id=$LAST_ITEM_ID")

if is_checkout_successful "$CHECKOUT_RESPONSE"; then
    CODE=$(extract_code "$CHECKOUT_RESPONSE")
    echo "Checkout successful. Code: $CODE"

    # Attempt to purchase the 11th item, which should fail
    echo "Attempting to purchase 11th item (should fail)..."
    PURCHASE_RESPONSE=$(curl -s -X POST "http://localhost:8080/purchase?code=$CODE")

    if is_purchase_successful "$PURCHASE_RESPONSE"; then
        echo "ERROR: 11th purchase succeeded, but it should have failed!"
        exit 1
    else
        echo "11th purchase failed as expected: $PURCHASE_RESPONSE"

        # Check if the failure reason is the maximum items limit
        if echo "$PURCHASE_RESPONSE" | grep -q "User has reached the maximum of 10 items per sale"; then
            echo "SUCCESS: Test passed. User is correctly limited to 10 items per sale."
        else
            echo "ERROR: 11th purchase failed, but for the wrong reason: $PURCHASE_RESPONSE"
            exit 1
        fi
    fi
else
    echo "ERROR: 11th checkout failed, but it should have succeeded: $CHECKOUT_RESPONSE"
    exit 1
fi

echo "All tests completed successfully."
