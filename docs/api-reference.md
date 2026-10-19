# Social & WhatsApp API Reference for a Sprout-Style Platform

Oct 19, 2026 · @Yahya

## Overview

Every core Sprout feature (publishing, a unified inbox, reporting) can be built on official APIs from Meta, LinkedIn and Google. Social listening across the open web cannot; it needs a separate data source. Each platform gates write access behind an app review, so access approval, not coding, is the long pole.

The table maps each Sprout feature to the APIs that power it. Later sections list the endpoints, permissions and limits per platform. Versions checked: Meta Graph API v26.0 (released July 29, 2026), LinkedIn versioned REST API, YouTube Data API v3.

| Sprout feature | Facebook | Instagram | LinkedIn | YouTube | WhatsApp |
| --- | --- | --- | --- | --- | --- |
| Publish and schedule | Pages API `/feed`, `/photos`, Video API | Content Publishing (`/media`, `/media_publish`) | Posts API, Images/Videos API | `videos.insert` | Not applicable |
| Unified inbox: comments | Comments edge + `feed` webhook | Comments + `comments` webhook | Comments API + webhooks | `commentThreads`, `comments` (polling) | Not applicable |
| Unified inbox: DMs | Messenger Platform | Instagram Messaging API | Not available to third parties | Not available | Cloud API |
| Mentions and tags | `mention` webhook | Mentions + `mentions` webhook | Org notifications | Not available | Not applicable |
| Reviews | `ratings` edge + webhook | Not applicable | Not applicable | Not applicable | Not applicable |
| Analytics | Page and Post Insights | Media and Account Insights | Org Share, Follower and Page Statistics | YouTube Analytics + Reporting API | Analytics endpoints, webhooks |
| Competitor benchmarks | Page Public Content Access (feature) | Business Discovery | Not available | Public `channels`/`videos` stats | Not applicable |
| Hashtag or keyword search | Not available | Hashtag Search (30 tags per 7 days) | Not available | `search.list` | Not applicable |

Rule of thumb: Meta and YouTube give you most of Sprout. LinkedIn gives publishing, comments and analytics only after approval for its Community Management API. WhatsApp is the India-specific addition, covered last.

## Facebook Pages (Meta Graph API)

The Pages API plus the Messenger Platform covers Facebook publishing, inbox, reviews and analytics. Every permission needs Meta App Review before customers outside your business can grant it ([Pages API overview](https://developers.facebook.com/docs/pages-api/overview)). Auth flow: Facebook Login for Business → user token → `GET /me/accounts` → per-Page access token.

### Endpoints

| Capability | Endpoint | Notes |
| --- | --- | --- |
| List Pages a user manages | `GET /me/accounts` | Returns Page IDs and Page tokens |
| Publish text/link post | `POST /{page-id}/feed` | `message`, `link` |
| Schedule post | `POST /{page-id}/feed` with `published=false` + `scheduled_publish_time` | 10 minutes to 30 days ahead ([Posts guide](https://developers.facebook.com/docs/pages-api/posts)) |
| Publish photo | `POST /{page-id}/photos` | `url` of the image |
| Publish video / Reel | Video API (`/{page-id}/videos`, Reels publishing) | Needs `publish_video` |
| Read Page posts | `GET /{page-id}/feed` | Paged |
| Edit / delete post | `POST /{post-id}` / `DELETE /{post-id}` | Edit only posts made by your app |
| Read comments | `GET /{post-id}/comments?fields=from,message` | `from` gives the commenter's Page-scoped ID |
| Reply to comment | `POST /{comment-id}/comments` | Comment as the Page ([Comments guide](https://developers.facebook.com/docs/pages-api/comments-mentions)) |
| @mention a commenter | `message` containing `@[PSID]` | Needs the Page Mentioning feature |
| Hide / delete comment | `POST /{comment-id}` with `is_hidden` / `DELETE /{comment-id}` | MODERATE task |
| Private reply to a comment | Messenger Send API with `recipient.comment_id` | One message, within 7 days of the comment ([policy](https://developers.facebook.com/docs/messenger-platform/policy/policy-overview)) |
| Send / receive Messenger DMs | `POST /{page-id}/messages`, `messages` webhook | 24-hour window; `HUMAN_AGENT` tag extends it to 7 days |
| Conversation history | `GET /{page-id}/conversations` | For backfilling the inbox |
| Reviews / recommendations | `GET /{page-id}/ratings`, `ratings` webhook | Star rating, review text, reviewer |
| Page insights | `GET /{page-id}/insights?metric=...` | Needs `read_insights`; data only for Pages with 100+ likes ([Insights ref](https://developers.facebook.com/docs/graph-api/reference/page/insights)) |
| Post insights | `GET /{post-id}/insights?metric=...` | Lifetime period |
| Lead ads (optional) | `leadgen` webhook | Useful for agencies in India |

### Webhooks to subscribe

All Page webhooks require the `pages_manage_metadata` permission, granted by a Page admin with the MODERATE task ([webhook reference](https://developers.facebook.com/docs/graph-api/webhooks/reference/page)).

- `feed`: new posts, comments, reactions, shares, edits and hides. No events for ad posts, but comments on ads do arrive.
- `mention`: posts and comments that mention the Page.
- `messages`, `message_echoes`, `message_reads`, `message_reactions`, `messaging_postbacks`: the Messenger inbox.
- `ratings`: new or edited reviews and replies to them.

### Insight metrics that still work

Meta is retiring reach-style metrics. The `_unique` reach metrics (for example `page_impressions_unique`, `post_impressions_unique`) are deprecated above v25.0, and a batch of Page Insights metrics was deprecated for all versions by June 15, 2026. Build reports on these instead:

| Use | Metric |
| --- | --- |
| Views (replaces reach) | `page_media_view`, `page_total_media_view_unique`; per post `post_media_view`, `post_total_media_view_unique` |
| Engagement | `page_post_engagements`, `post_reactions_by_type_total`, `post_clicks` |
| Followers | `page_follows`, `page_daily_follows_unique`, `page_daily_unfollows_unique` |
| Video | `post_video_views`, `post_video_avg_time_watched`, `post_video_retention_graph` |
| Profile visits and CTA | `page_views_total`, `page_total_actions` |

Limits: only 2 years of history, at most 90 days per `since`/`until` request, most metrics refresh once every 24 hours, and Reels interactions are not included in Page Insights.

### Permissions to request

`pages_show_list`, `pages_read_engagement`, `pages_read_user_content`, `pages_manage_posts`, `pages_manage_engagement`, `pages_manage_metadata`, `pages_messaging`, `read_insights`, `publish_video`, plus `business_management` for Business Manager assets. The Page Public Content Access feature (for competitor Pages) requires its own review and is hard to get approved.

## Instagram (Instagram Platform API)

Instagram supports publishing, comment moderation, DMs, mentions and insights for professional (Business or Creator) accounts only. A Sprout clone should use the Facebook Login setup: only it offers hashtag search, product tagging, collaborators, partnership-ad labels and the `story_insights` webhook ([Instagram Platform overview](https://developers.facebook.com/docs/instagram-platform/overview)). Serving accounts you don't own needs Advanced Access, which requires App Review plus Business Verification.

### Pick a login setup

|  | Instagram API with Facebook Login | Instagram API with Instagram Login |
| --- | --- | --- |
| Account requirement | IG professional account linked to a Facebook Page | IG professional account, no Page needed |
| Host | `graph.facebook.com` | `graph.instagram.com` |
| Token | Facebook User or Page token | Instagram User token (60-day long-lived, refreshable) |
| Hashtag search, product tags, collaborators | Yes | No |
| DMs via | Messenger Platform (Instagram Messaging API) | Instagram API directly |
| Best for | Brands and agencies (most Indian businesses already link IG to a Page) | Creators without a Page |

### Endpoints

| Capability | Endpoint | Notes |
| --- | --- | --- |
| Find the IG account | `GET /{page-id}?fields=instagram_business_account` | Facebook Login setup |
| Create media container | `POST /{ig-id}/media` | `image_url` or `video_url`; `media_type` = `REELS`, `STORIES` or `CAROUSEL`; media must be on a public URL ([Content Publishing](https://developers.facebook.com/docs/instagram-platform/content-publishing)) |
| Carousel | `POST /{ig-id}/media` with `media_type=CAROUSEL`, `children` | Up to 10 items |
| Resumable video upload | `POST https://rupload.facebook.com/ig-api-upload/{container-id}` | Facebook Login only |
| Check container status | `GET /{container-id}?fields=status_code` | `FINISHED`, `IN_PROGRESS`, `ERROR`, `EXPIRED` (24 h) |
| Publish | `POST /{ig-id}/media_publish` with `creation_id` | No native scheduling: your scheduler calls this at post time |
| Check publishing quota | `GET /{ig-id}/content_publishing_limit` | 100 API posts per 24 h per account; carousel counts as 1 (the carousel section of the same page says 50, so enforce 50 to be safe) |
| Extras on container | `alt_text` (images), `is_ai_generated`, `trial_params` (trial Reels), `user_tags`, `collaborators`, `branded_content_sponsor_ids` | JPEG only; no filters or shopping tags |
| List media | `GET /{ig-id}/media` | Own posts, Reels, carousels |
| Read comments | `GET /{media-id}/comments` | Organic only |
| Reply to comment | `POST /{comment-id}/replies` |  |
| Hide / delete comment | `POST /{comment-id}?hide=true` / `DELETE /{comment-id}` |  |
| Turn comments off | `POST /{media-id}?comment_enabled=false` |  |
| Private reply to comment | Send API with `recipient.comment_id` | 750 calls/hour per account |
| Send / receive DMs | `POST /{ig-id}/messages` (IG Login) or `POST /{page-id}/messages` (FB Login) | 24 h window; `HUMAN_AGENT` tag extends to 7 days |
| Conversation history | `GET /{ig-id}/conversations` | 2 calls/second per account |
| Media you're tagged in | `GET /{ig-id}/tags` | Mentions in photos |
| Comment or caption @mentions | `mentioned_comment`, `mentioned_media` fields; `mentions` webhook |  |
| Competitor stats | `GET /{ig-id}?fields=business_discovery.username(handle){followers_count,media_count,media{like_count,comments_count}}` | Public data of other Business/Creator accounts |
| Hashtag search | `GET /ig_hashtag_search?user_id=&q=` → `/{hashtag-id}/top_media`, `/recent_media` | 30 unique hashtags per account per rolling 7 days; needs the Instagram Public Content Access feature ([Hashtag Search](https://developers.facebook.com/docs/instagram-platform/instagram-api-with-facebook-login/hashtag-search)) |
| Media insights | `GET /{media-id}/insights?metric=` | See metrics below |
| Account insights | `GET /{ig-id}/insights?metric=&period=day&metric_type=total_value` | See metrics below |

### Webhooks to subscribe

Enable with `POST /me/subscribed_apps?subscribed_fields=...`. The app must be Live, the IG account must be public for comment and mention events, and Meta retries failed deliveries for up to 36 hours ([Instagram webhooks](https://developers.facebook.com/docs/instagram-platform/webhooks)).

- `comments`, `live_comments`: new comments (Advanced Access required).
- `mentions`: @mentions in captions and comments (Facebook Login setup).
- `messages`, `message_reactions`, `messaging_seen`, `messaging_postbacks`, `messaging_referral`: the DM inbox.
- `story_insights`: Story metrics before the Story expires (Facebook Login only).

### Insight metrics that still work

`impressions` is gone for media created after July 2, 2024 and for accounts from April 21, 2025. Use `views` instead ([Media Insights](https://developers.facebook.com/docs/instagram-platform/reference/instagram-media/insights), [Account Insights](https://developers.facebook.com/docs/instagram-platform/api-reference/instagram-user/insights)).

| Level | Metrics |
| --- | --- |
| Post (FEED) | `views`, `reach`, `likes`, `comments`, `shares`, `saved`, `reposts`, `total_interactions`, `profile_visits`, `follows` |
| Reel | `views`, `reach`, `ig_reels_avg_watch_time`, `ig_reels_video_view_total_time`, `reels_skip_rate`, `crossposted_views`, `likes`, `comments`, `shares`, `saved` |
| Story | `views`, `reach`, `replies`, `navigation`, `link_clicks`, `shares`, `profile_visits` (24 h only; under 5 viewers returns an error) |
| Account | `views`, `reach`, `accounts_engaged`, `total_interactions`, `likes`, `comments`, `shares`, `saves`, `follows_and_unfollows`, `profile_links_taps` |
| Audience | `follower_demographics`, `engaged_audience_demographics` (age, city, country, gender; top 45 only; 100+ followers) |

Limits: media metrics can lag up to 48 hours and are kept 2 years. Organic metrics exclude ad interactions; `total_likes`, `total_comments` and `total_views` include them (Facebook Login only).

### Permissions to request

Facebook Login: `instagram_basic`, `instagram_content_publish`, `instagram_manage_comments`, `instagram_manage_insights`, `instagram_manage_messages`, `pages_show_list`, `pages_read_engagement`, `pages_manage_metadata`, plus the Human Agent and Instagram Public Content Access features. Instagram Login equivalents: `instagram_business_basic`, `instagram_business_content_publish`, `instagram_business_manage_comments`, `instagram_business_manage_messages`, `instagram_business_manage_insights`.

Rate limit: non-messaging calls are capped at 4,800 × the account's impressions in the last 24 hours, per app-user pair.

## LinkedIn (Community Management API)

LinkedIn gives company-page publishing, comments, reactions, notifications and page analytics through one vetted product, the Community Management API. Page DMs are not available to third parties, and there is no native post scheduling. Access starts at a Development tier (500 calls per app and 100 per member per day) and needs a screencast review to reach Standard tier ([Community Management overview](https://learn.microsoft.com/en-us/linkedin/marketing/community-management/community-management-overview?view=li-lms-2026-08)).

Every call needs the headers `LinkedIn-Version: YYYYMM` (for example `202609`) and `X-Restli-Protocol-Version: 2.0.0`. Each monthly version is sunset after about a year; version 202511 sunsets November 16, 2026. Base URL: `https://api.linkedin.com/rest/`.

### Endpoints

| Capability | Endpoint | Notes |
| --- | --- | --- |
| Pages the user can manage | `GET /organizationAcls?q=roleAssignee` | Returns org URNs and the member's role (ADMINISTRATOR, CONTENT\_ADMIN, etc.) |
| Organization lookup | `GET /organizations/{id}`, `GET /organizations?q=vanityName` | Name, logo, vanity URL |
| Upload image | `POST /images?action=initializeUpload` → PUT to upload URL | Returns `urn:li:image:{id}` |
| Upload video | `POST /videos?action=initializeUpload` → chunked PUTs → `finalizeUpload` | Returns `urn:li:video:{id}` |
| Upload document (PDF carousel) | `POST /documents?action=initializeUpload` | Returns `urn:li:document:{id}`; popular "PDF carousel" format |
| Publish post | `POST /posts` with `author: urn:li:organization:{id}`, `lifecycleState: PUBLISHED` | Text, image, video, document, article, multi-image and poll for organic posts ([Posts API](https://learn.microsoft.com/en-us/linkedin/marketing/community-management/shares/posts-api?view=li-lms-2026-09)) |
| Schedule post | Not supported | Only `PUBLISHED` is accepted at creation; your scheduler calls `POST /posts` at post time |
| Mentions and hashtags | `@[Name](urn:li:organization:{id})` and `#tag` in `commentary` | Name must match exactly |
| Targeted organic post | `distribution.targetEntities` (geo, seniority, industry) | Audience must exceed 300 followers |
| Reshare | `POST /posts` with `reshareContext.parent` |  |
| List page posts | `GET /posts?q=author&author={org-urn}` | Max 100 per page; needs `r_organization_social` |
| Edit / delete post | `POST /posts/{urn}` with `X-RestLi-Method: PARTIAL_UPDATE` / `DELETE /posts/{urn}` | Only `commentary`, CTA and a few fields are editable |
| Read and write comments | `GET`/`POST /socialActions/{post-urn}/comments` | Reply as the organization |
| Reactions | `POST /reactions?actor={org-urn}`, `GET /reactions/(entity:{urn})?q=entity` | Like, celebrate, love, insightful, etc. |
| Comment and reaction counts; disable comments | Social Metadata API | Replaces the old socialActions summary |
| Engagement notifications (webhook) | `PUT /eventSubscriptions/(...,eventType:ORGANIZATION_SOCIAL_ACTION_NOTIFICATIONS)` | Pushes LIKE, COMMENT, SHARE, SHARE\_MENTION, COMMENT\_EDIT, COMMENT\_DELETE; retries every 5 min for 8 h ([notifications](https://learn.microsoft.com/en-us/linkedin/marketing/community-management/organizations/organization-social-action-notifications?view=li-lms-2026-09)) |
| Engagement notifications (pull) | `GET /organizationalEntityNotifications?q=criteria&actions=List(COMMENT,SHARE_MENTION)` | Kept 60 days; use for backfill |
| @mention search | People Typeahead API | Find members to tag in page posts |
| Post analytics | `GET /organizationalEntityShareStatistics?q=organizationalEntity` | Impressions, unique impressions, clicks, likes, comments, shares, engagement rate; lifetime or daily |
| Follower analytics | `GET /organizationalEntityFollowerStatistics?q=organizationalEntity` | Totals plus breakdowns by function, seniority, industry, region, company size |
| Page analytics | `GET /organizationPageStatistics?q=organization` | Page views and button clicks by section and device |
| Video analytics | Video Analytics API | Watch time, views, viewers |

### What LinkedIn does not give you

- **Page inbox or DMs**: the Messages API is restricted to approved partners.
- **Personal-profile reads**: `r_member_social` is closed to new requests, so you can post to a member profile (`w_member_social`) but not read its posts or stats.
- **Keyword or hashtag search** across LinkedIn: no listening API.
- **Competitor page stats**: analytics only cover pages the user administers.

### Scopes to request

`w_organization_social`, `r_organization_social`, `rw_organization_admin` (notifications, stats, page management), `r_organization_admin`, plus `w_member_social` if users also post to their personal profiles. Posting scopes require the member to be ADMINISTRATOR, CONTENT\_ADMIN or DIRECT\_SPONSORED\_CONTENT\_POSTER on the page.

Tokens last 60 days. Programmatic refresh tokens are only issued to approved partners, so plan a re-authentication prompt every 60 days until you have them.

## YouTube (Data API v3, Analytics API, Reporting API)

YouTube covers uploads with native scheduling, comment moderation and deep analytics. It has no DMs and no comment webhooks, so the inbox must poll. Quota, not approval, is the main constraint. Since June 2026, `videos.insert` and `search.list` each have their own bucket of 100 calls per day, plus 10,000 units per day for everything else ([Quota Calculator](https://developers.google.com/youtube/v3/determine_quota_cost)).

Uploads from unverified API projects created after July 28, 2020 are forced to private. Pass Google's API compliance audit before launch, or every scheduled video stays private.

### Endpoints (Data API v3)

| Capability | Method | Quota cost |
| --- | --- | --- |
| Find the user's channel | `channels.list?mine=true` | 1 unit |
| Upload video / Short | `videos.insert` (resumable upload) | 1 call from the 100/day upload bucket |
| Schedule video | `videos.insert` with `status.privacyStatus=private` + `status.publishAt` | Same call; YouTube publishes it |
| Edit title, description, tags | `videos.update` | 50 |
| Custom thumbnail | `thumbnails.set` | 50 |
| Captions | `captions.insert` / `captions.update` | 400 / 450 |
| Add to playlist | `playlistItems.insert` | 50 |
| Delete video | `videos.delete` | 50 |
| List channel videos | `playlistItems.list` on the channel's uploads playlist | 1 per page |
| Video stats (views, likes, comments) | `videos.list?part=statistics` or `videos.batchGetStats` (new Sept 2026, own bucket) | 1 |
| All comments on the channel | `commentThreads.list?allThreadsRelatedToChannelId=` | 1 per page; poll for the inbox |
| Comments held for review | `commentThreads.list?moderationStatus=heldForReview` | 1 |
| Reply to comment | `comments.insert` with `parentId` | 50 |
| Approve / reject / hold comment | `comments.setModerationStatus` | 50 |
| Delete own comment | `comments.delete` | 50 |
| Competitor channel stats | `channels.list?id=` and `videos.list?id=` (public stats) | 1 |
| Keyword search (light listening) | `search.list` | 1 call from the 100/day search bucket |
| New-upload notifications | PubSubHubbub push for a channel's feed | Free; covers new videos only, not comments |

A single reply costs 50 units, so 10,000 units allow about 200 replies per day across all customers. Request a quota extension early through the [Quota Extension Form](https://developers.google.com/youtube/v3/determine_quota_cost).

### Analytics

- **YouTube Analytics API** (`reports.query`): on-demand reports for dashboards. Common metrics: `views`, `estimatedMinutesWatched`, `averageViewDuration`, `averageViewPercentage`, `likes`, `comments`, `shares`, `subscribersGained`, `subscribersLost`. Dimensions: `day`, `video`, `country`, `ageGroup`, `gender`, `insightTrafficSourceType`, `deviceType`.
- **YouTube Reporting API**: scheduled bulk CSV jobs, better for nightly sync of many channels without burning quota.

Shorts views have been counted as plays (every start or replay) since March 31, 2025, so Shorts and long-form views are not comparable with older data.

### OAuth scopes

`youtube.upload` (upload only), `youtube.force-ssl` (comments and moderation), `youtube.readonly`, `yt-analytics.readonly`. Google issues refresh tokens, so YouTube connections do not need periodic re-login once the OAuth consent screen is verified.

## WhatsApp Business Platform (for businesses in India)

For an Indian product, WhatsApp is the channel that closes sales, so it needs more depth than Sprout's basic integration. Use the Cloud API (Meta-hosted) and onboard customers through Embedded Signup as a Meta Tech Provider. Customers whose billing country is India must move every WhatsApp Business Account (WABA) to INR billing by December 31, 2026, or Meta stops delivering their messages from January 1, 2027 ([WhatsApp pricing](https://developers.facebook.com/documentation/business-messaging/whatsapp/pricing)).

### Endpoints

| Capability | Endpoint / product | Why it matters in India |
| --- | --- | --- |
| Onboard a customer's number | Embedded Signup (Facebook Login for Business flow) | Lets SMBs connect their own WABA in minutes |
| Register number, 2-step PIN | `POST /{phone-number-id}/register` | Required before sending |
| Business profile | `POST /{phone-number-id}/whatsapp_business_profile` | About, address, website, category |
| Send any message | `POST /{phone-number-id}/messages` | Text, image, video, document, audio, location, contacts, reactions |
| Interactive messages | `type: interactive` (reply buttons, lists, CTA URL, location request) | Menus and quick replies for support bots |
| Template messages | `type: template` | Only way to message outside the 24 h window |
| Create / list templates | `POST` / `GET /{waba-id}/message_templates` | Marketing, utility and authentication categories; Meta can recategorize |
| Upload / fetch media | `POST /{phone-number-id}/media`, `GET /{media-id}` |  |
| Mark read, typing indicator | `POST /{phone-number-id}/messages` with `status: read` | Inbox UX |
| Product catalog messages | Single-product and multi-product messages linked to a Meta catalog | Catalog shopping inside chat |
| WhatsApp Flows | `POST /{waba-id}/flows` | In-chat forms: lead capture, bookings, KYC |
| Payments (India only) | `order_details` and `order_status` messages via the [Payments API India](https://developers.facebook.com/docs/whatsapp/cloud-api/payments-api/payments-in) | UPI intent with any gateway; full native flow (cards, NetBanking, refunds) via Razorpay, PayU, BillDesk or Zaakpay |
| Marketing Messages API for WhatsApp | Separate product for broadcasts | Optimized delivery; optional max price per marketing message from 2026 |
| Business calling | WhatsApp Business Calling API | Voice calls in-chat (separate pricing) |
| Block abusive users | Block Users API | Inbox hygiene |
| Analytics | `GET /{waba-id}?fields=analytics`, `pricing_analytics`, template analytics | Cost and delivery dashboards per customer |
| Messaging limit | `GET /{phone-number-id}?fields=whatsapp_business_manager_messaging_limit` | See limits below |

### Webhooks to subscribe

- `messages`: inbound messages plus delivery statuses (sent, delivered, read, failed), each with a `pricing` object showing billable or free.
- `message_template_status_update` and template category updates: approvals, rejections, recategorization.
- `account_update`: includes volume-tier changes (`VOLUME_BASED_PRICING_TIER_UPDATE`).
- `business_capability_update`, `account_alerts`: messaging-limit increases and quality warnings.

### India pricing (per delivered message, before 18% GST)

| Category | Rate | When charged |
| --- | --- | --- |
| Marketing template | ₹0.8631 | Always (raised about 10% on January 1, 2026) |
| Utility template | ₹0.115 | Outside the 24 h window; see the October 2026 note |
| Authentication template | ₹0.115 | Always; volume discounts above 750,000/month |
| Authentication-international | ₹2.4971 | OTPs to Indian users from a WABA registered outside India |
| Service (free-form reply in 24 h window) | Free, see note |  |
| Anything after a Click-to-WhatsApp ad reply | Free for 72 h | Free entry point window |

INR rates are as reported by several Indian BSPs, for example [ChatMaxima](https://chatmaxima.com/whatsapp-api-pricing/india/); check your own rate card in Business Manager. Meta changes rates only on January 1, April 1, July 1 or October 1.

**October 1, 2026 change (verify):** many BSPs, quoting Meta's pricing page, report that service replies are now charged at the utility rate after 1,000 free per business phone number per month, and that utility templates inside the window are charged too ([respond.io](https://respond.io/blog/whatsapp-pricing-change-2026)). The copy of Meta's developer pricing page I could open still shows the old free rules, so confirm before you model costs. Numbers without a payment method reportedly stop sending service messages after the free 1,000.

### Limits and policies

- **Messaging limits**: new business portfolios can message 250 unique users per rolling 24 h outside the service window. It rises to 2,000 after verification or 2,000 high-quality template sends in 30 days, then scales automatically. Limits are shared by all numbers in a portfolio ([Messaging limits](https://developers.facebook.com/docs/whatsapp/messaging-limits)).
- **Template quality**: low-quality ratings pause templates and block scaling, so build quality monitoring into the product.
- **AI providers**: Meta has a separate pricing policy for AI providers on WhatsApp, effective February 16, 2026; review it before adding an AI agent.

## Access, tokens and gaps

Plan the approvals before the code: each platform has its own review, and production use is blocked until it passes.

| Platform | Approval needed | Token lifetime | Inbox delivery |
| --- | --- | --- | --- |
| Facebook | Meta App Review per permission + Business Verification | Long-lived Page tokens (no expiry while the user's grant is valid) | Webhooks |
| Instagram | Advanced Access via App Review + Business Verification | 60 days (IG Login, refreshable); Page token via FB Login | Webhooks |
| LinkedIn | Community Management API: Development tier, then Standard tier with screencast | 60 days; refresh tokens only for approved partners | Webhooks (notifications) + pull |
| YouTube | OAuth consent verification + API compliance audit (or uploads stay private) | Refresh tokens, no forced re-login | Polling only |
| WhatsApp | Meta Tech Provider onboarding, Business Verification, template approvals | System-user tokens (non-expiring) | Webhooks |

One Meta app can cover Facebook, Instagram and WhatsApp, so a single App Review submission can bundle all three.

### What official APIs cannot give you

- **Open-web social listening** (brand mentions on X, Reddit, news, untagged posts): no platform here offers it. Sprout uses its NewsWhip acquisition and paid data providers; you would need a third-party data vendor.
- **LinkedIn and YouTube DMs**: not available.
- **Competitor analytics on LinkedIn**: not available; Facebook needs the hard-to-get Page Public Content Access feature, while Instagram Business Discovery and public YouTube stats are easy.
- **TikTok**: excluded here since it is banned in India; X and Threads are separate APIs not covered.

### Sources

- [Meta Graph API changelog v26.0](https://developers.facebook.com/docs/graph-api/changelog/version26.0)
- [Pages API overview](https://developers.facebook.com/docs/pages-api/overview), [Posts](https://developers.facebook.com/docs/pages-api/posts), [Comments](https://developers.facebook.com/docs/pages-api/comments-mentions), [Page Insights](https://developers.facebook.com/docs/graph-api/reference/page/insights), [Page webhooks](https://developers.facebook.com/docs/graph-api/webhooks/reference/page)
- [Messenger and IG messaging policy](https://developers.facebook.com/docs/messenger-platform/policy/policy-overview)
- [Instagram Platform overview](https://developers.facebook.com/docs/instagram-platform/overview), [Content Publishing](https://developers.facebook.com/docs/instagram-platform/content-publishing), [Webhooks](https://developers.facebook.com/docs/instagram-platform/webhooks), [Hashtag Search](https://developers.facebook.com/docs/instagram-platform/instagram-api-with-facebook-login/hashtag-search), [Media Insights](https://developers.facebook.com/docs/instagram-platform/reference/instagram-media/insights), [Account Insights](https://developers.facebook.com/docs/instagram-platform/api-reference/instagram-user/insights)
- [LinkedIn Community Management overview](https://learn.microsoft.com/en-us/linkedin/marketing/community-management/community-management-overview?view=li-lms-2026-08), [Posts API](https://learn.microsoft.com/en-us/linkedin/marketing/community-management/shares/posts-api?view=li-lms-2026-09), [Organization notifications](https://learn.microsoft.com/en-us/linkedin/marketing/community-management/organizations/organization-social-action-notifications?view=li-lms-2026-09)
- [YouTube Data API quota calculator](https://developers.google.com/youtube/v3/determine_quota_cost)
- [WhatsApp pricing](https://developers.facebook.com/documentation/business-messaging/whatsapp/pricing), [Messaging limits](https://developers.facebook.com/docs/whatsapp/messaging-limits), [Payments API India](https://developers.facebook.com/docs/whatsapp/cloud-api/payments-api/payments-in)
- India WhatsApp rates: [ChatMaxima](https://chatmaxima.com/whatsapp-api-pricing/india/); October 2026 change: [respond.io](https://respond.io/blog/whatsapp-pricing-change-2026)
