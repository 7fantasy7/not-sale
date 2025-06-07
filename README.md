# NOT Back Contest - Flash Sale Service

```text
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

TODO

## License

This project is licensed under the Apache License 2.0.
You are free to use, modify, and distribute this software for any purpose, including commercial applications, as long as
you comply with the terms of the license.