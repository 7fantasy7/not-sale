package main

import (
	"fmt"
	"log"

	"not-sale-back/internal/config"
	"not-sale-back/internal/server"
)

func main() {
	logo := `
           ##                            ( )_
          #||#            ___      _ //  |  _)   ___    _ //  (_)   ___
         # || #         /  _  \  / _//\  | |   / ___) / _//\  | | /  _  \
        #  ||  #        | ( ) | ( (//  ) | |_ ( (___ ( (//  ) | | | ( ) |
       ##  ||  ##       (_) (_)  \//__/   \__) \____) \//__/  (_) (_) (_)
      ##   ||   ##               //                   //                 
     ##    ||    ##                    
    ##     ||     ##    Probably nothing.             
   ##################   `
	fmt.Println(logo)

	cfg := config.NewConfig()

	app, err := server.NewServer(cfg)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}
	defer app.Close()

	if err := app.Run(); err != nil {
		log.Fatalf("Failed to run server: %v", err)
	}
}
