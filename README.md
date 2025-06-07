# NOT Back Contest - Flash Sale Service

For https://contest.notco.in/dev-backend

```text IsntThatAPerfectEasterEgg
            ##                            ( )_
           #||#            ___      _ //  |  _)   ___    _ //  (_)   ___
          # || #         /  _  \  / _//\  | |   / ___) / _//\  | | /  _  \
         #  ||  #        | ( ) | ( (//  ) | |_ ( (___ ( (//  ) | | | ( ) |
        ##  ||  ##       (_) (_)  \//__/   \__) \____) \//__/  (_) (_) (_)
       ##   ||   ##               //                   //                 
      ##    ||    ##                    
     ##     ||     ##    Probably nothing.             
    ##################   
```

## About

A high-throughput flash-sale service built with Go, Redis, and PostgreSQL.<br>
This service sells exactly 10,000 items every hour, <br>
with a two-step purchase process to ensure reliability and prevent overselling.

## Task Description

1. Every hour, a new flash sale of 10,000 items begins. Each item’s name and image are generated at runtime
2. Implement a sale flow that:

- Receives a first request: `POST /checkout?user_id=%user_id%&id=%item_id%`,<br> generates a unique code for the user
  and returns the code. All checkout attempts must be persisted
- Receives a second request: `POST /purchase?code=%code%`, verifies the code, and "sells" the item to the user
- Ensures exactly 10,000 items are sold each sale—no over‑ or underselling
- Limits each user to a maximum of 10 items per sale

## Technical details

### Assumptions (beyond the brief)

- Even next sale is started, the previous sale is still active until there are items available.
- User can checkout more then 10 times on the same sale, but only 10 items can be purchased. <br>Imaging going back or
  refreshing the page scenario.
- The service doesn't contain a state for performance "hacks", assuming it can be run in clustered mode.
- 10,000 items are not that big of a deal, so preferring strong consistency over performance.
- Not overcomplicating the code with unnecessary abstractions, keeping it simple and readable.

### Libraries and Technologies

- [Go-chi router](https://github.com/go-chi/chi/v5) - A lightweight, idiomatic and composable router for building Go
  HTTP services.
- [Go-redis](https://github.com/redis/go-redis) — Redis client for Go
- [Pgx](https://github.com/jackc/pgx) — PostgreSQL driver and toolkit for Go, best choice even it brings more deps, as lig/pq is not well supported

TODO

## Building and Running

### Prerequisites

- Docker
- Docker Compose

### Build and Run

1. Clone the repository:

```bash
git clone https://github.com/7fantasy7/not-sale-back.git
cd not-sale-back
```

2. Start the services using Docker Compose:

```bash
docker-compose up -d --build
```

3. The service will be available at http://localhost:8080

## Performance Testing

The project includes performance tests to evaluate the application's behavior under load. These tests focus on the checkout and purchase flow, simulating multiple concurrent users.

### k6 Performance Tests

Project uses [k6](https://k6.io/) for performance testing, as it allows scripting to verify real user scenarios (checkout + purchase flow)

### To run the performance tests:

### Install k6

##### macOS
```bash
brew install k6
```
#### Other platforms
see [installation instructions](https://grafana.com/docs/k6/latest/set-up/install-k6/)


### Run the tests:

```bash
K6_WEB_DASHBOARD=true K6_WEB_DASHBOARD_EXPORT=html-report.html k6 run dev/perf.js
```
### View the results
Open the generated `dev/html-report.html` file in your web browser to view the performance test results.

## License

This project is licensed under the Apache License 2.0.
You are free to use, modify, and distribute this software for any purpose, including commercial applications, as long as
you comply with the terms of the license.
