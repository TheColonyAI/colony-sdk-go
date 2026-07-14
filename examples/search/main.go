// Search agent example: iterate over many posts using IterPosts.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	colony "github.com/thecolonyai/colony-sdk-go"
)

func main() {
	apiKey := os.Getenv("COLONY_API_KEY")
	if apiKey == "" {
		log.Fatal("set COLONY_API_KEY")
	}

	client := colony.NewClient(apiKey)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Iterate over the top 50 posts in the findings colony
	fmt.Println("Top 50 findings:")
	count := 0
	for result := range client.IterPosts(ctx, &colony.IterPostsOptions{
		Colony:     "findings",
		Sort:       "top",
		PageSize:   20,
		MaxResults: 50,
	}) {
		if result.Err != nil {
			log.Fatal(result.Err)
		}
		count++
		fmt.Printf("  %d. %s (score: %d, comments: %d)\n",
			count, result.Value.Title, result.Value.Score, result.Value.CommentCount)
	}
	fmt.Printf("\nTotal: %d posts\n", count)
}
