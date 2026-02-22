#!/usr/bin/env python3
"""Fetch top 10 tech news headlines from RSS feeds."""

import feedparser
from datetime import datetime, timezone, timedelta

def fetch_feed(url, source_name):
    """Parse an RSS feed and return entries with headline, source, and date."""
    try:
        feed = feedparser.parse(url)
        entries = []
        for entry in feed.entries[:5]:
            # Get published date
            pub_date = None
            if hasattr(entry, 'published_parsed') and entry.published_parsed:
                pub_date = datetime(*entry.published_parsed[:6], tzinfo=timezone.utc)
            elif hasattr(entry, 'updated_parsed') and entry.updated_parsed:
                pub_date = datetime(*entry.updated_parsed[:6], tzinfo=timezone.utc)
            
            entries.append({
                'headline': entry.title,
                'source': source_name,
                'date': pub_date,
                'link': entry.link if hasattr(entry, 'link') else ''
            })
        return entries
    except Exception as e:
        print(f"Error fetching {source_name}: {e}")
        return []

def main():
    # Define RSS feeds from reputable tech sources
    feeds = [
        ('https://techcrunch.com/feed/', 'TechCrunch'),
        ('https://www.theverge.com/rss/index.xml', 'The Verge'),
        ('https://www.wired.com/feed/rss', 'Wired'),
        ('https://arstechnica.com/feed/', 'Ars Technica'),
        ('https://feeds.bbci.co.uk/news/technology/rss.xml', 'BBC Tech'),
    ]
    
    all_entries = []
    cutoff_time = datetime.now(timezone.utc) - timedelta(hours=24)
    
    for url, source in feeds:
        entries = fetch_feed(url, source)
        for entry in entries:
            # Include entries from last 24 hours or if no date available
            if entry['date'] is None or entry['date'] >= cutoff_time:
                all_entries.append(entry)
    
    # Sort by date (newest first), handling None dates
    all_entries.sort(key=lambda x: x['date'] or datetime.min.replace(tzinfo=timezone.utc), reverse=True)
    
    # Take top 10
    top_10 = all_entries[:10]
    
    print(f"Top {len(top_10)} Technology News Headlines\n")
    print("=" * 80)
    
    for i, item in enumerate(top_10, 1):
        date_str = item['date'].strftime('%Y-%m-%d %H:%M UTC') if item['date'] else 'Unknown'
        print(f"{i}. {item['headline']}")
        print(f"   Source: {item['source']}")
        print(f"   Date: {date_str}")
        print()
    
    return top_10

if __name__ == '__main__':
    main()
