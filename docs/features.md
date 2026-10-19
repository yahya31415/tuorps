# Tuorps Feature List

Oct 19, 2026 · @Yahya

## Overview

Tuorps is a social media and WhatsApp management platform for Indian businesses and agencies. It covers Facebook, Instagram, LinkedIn, YouTube and WhatsApp, plus Google Business Profile reviews. Its edge over Sprout is WhatsApp-first engagement and commerce, Indian languages, INR/GST/UPI billing and SMB pricing.

Every feature lists what powers it, taken from the companion API reference doc:

- **API**: an official platform endpoint (named in the table).
- **Internal**: built in Tuorps with no external API.
- **Vendor**: a paid third-party service.

Phase guide: **MVP** ships first, **V2** within 6 months of launch, **Later** once demand is proven. Phases are a proposal for the team to adjust.

## Publishing and content calendar

Tuorps must run its own scheduler: only Facebook (up to 30 days ahead) and YouTube (`publishAt`) schedule natively, while Instagram and LinkedIn publish only at call time.

| Feature | What it does | Powered by | Phase |
| --- | --- | --- | --- |
| Multi-network composer | One draft, tailored per network (caption, media, first comment) | Internal | MVP |
| Facebook posts | Text, link, photo, video, Reels | API: Pages `/feed`, `/photos`, Video API | MVP |
| Instagram posts | Image, carousel (10 items), Reels, Stories | API: `/media` + `/media_publish` | MVP |
| LinkedIn page posts | Text, image, video, PDF document, multi-image, poll, article | API: `/posts`, Images/Videos/Documents API | MVP |
| YouTube uploads | Long-form and Shorts, thumbnail, playlist, captions | API: `videos.insert`, `thumbnails.set` | MVP |
| Scheduler | Queue posts for a date and time per network | Internal + native schedule on FB/YouTube | MVP |
| Content calendar | Month/week view, drag to reschedule, filter by network or campaign | Internal | MVP |
| Quota guard | Block scheduling past limits (IG \~50/day, YouTube 100 uploads/day per project) | API: `content_publishing_limit` + internal | MVP |
| Publish status and retry | Track container status, retry failures, alert on errors | API: container `status_code` + internal | MVP |
| Media library | Store, tag and reuse images, videos, brand assets | Internal (S3-compatible storage) | MVP |
| Image and video checks | Validate JPEG-only for IG, aspect ratio, size, duration before publish | Internal | MVP |
| Link shortener with UTM | Auto-add UTM tags; branded short links | Internal | MVP |
| First comment | Auto-post a comment after publishing (hashtags, links) | API: comments endpoints | MVP |
| Instagram extras | Alt text, user tags, collaborators, AI-content label, trial Reels | API: `/media` parameters | V2 |
| LinkedIn targeted posts | Show a post to followers by location, seniority, industry (300+ audience) | API: `distribution.targetEntities` | V2 |
| Post edit and delete | Edit captions after publishing where allowed | API: FB/LinkedIn update, YouTube `videos.update` | V2 |
| Recurring and evergreen posts | Re-share top content on a schedule | Internal | V2 |
| Bulk upload | Schedule many posts from CSV | Internal | V2 |
| Best time to post | Suggest slots from each account's own engagement history | Internal analytics | V2 |
| Content campaigns | Group posts under a campaign with goals and tags | Internal | V2 |
| RSS / blog auto-post | Turn new blog posts into scheduled drafts | Internal | Later |
| Canva and Google Drive import | Pull designs and files into the composer | Vendor APIs | Later |

## Unified inbox and engagement

One inbox brings together comments, DMs, mentions, reviews and WhatsApp chats from every connected account. Webhooks feed Facebook, Instagram, LinkedIn and WhatsApp in real time; YouTube comments arrive by polling, so they lag by a few minutes.

| Feature | What it does | Powered by | Phase |
| --- | --- | --- | --- |
| Facebook comments | Read, reply, hide, delete, @mention commenter | API: comments edge + `feed` webhook | MVP |
| Messenger DMs | Two-way chat in the 24 h window; 7 days with human-agent tag | API: Messenger Platform + `messages` webhook | MVP |
| Instagram comments | Read, reply, hide, delete, turn comments off per post | API: comments + `comments` webhook | MVP |
| Instagram DMs | Two-way chat, media, reactions | API: Instagram Messaging + `messages` webhook | MVP |
| Private reply | Reply to a public FB/IG comment in DM (once, within 7 days) | API: Send API with `comment_id` | MVP |
| LinkedIn comments | Read and reply as the company page | API: `socialActions` comments + notifications webhook | MVP |
| YouTube comments | Read, reply, approve or reject held comments | API: `commentThreads`, `comments.setModerationStatus` | MVP |
| WhatsApp chats | Two-way chat, templates outside 24 h, media, buttons | API: Cloud API + `messages` webhook | MVP |
| Facebook reviews | Read and reply to recommendations | API: `ratings` edge + webhook | MVP |
| Google reviews | Read and reply to Google Business Profile reviews (key for local businesses) | API: Google Business Profile Reviews API (needs access approval) | V2 |
| Mentions stream | Posts and comments that tag the brand | API: FB `mention`, IG `mentions`, LinkedIn `SHARE_MENTION` | MVP |
| Conversation threading | Group every message from one person per channel into a single thread | Internal | MVP |
| Assign and status | Assign to teammate; open, pending, closed | Internal | MVP |
| Tags and notes | Label conversations (complaint, lead, order); internal notes | Internal | MVP |
| Saved replies | Reusable answers with variables, in several languages | Internal | MVP |
| Collision detection | Show when a teammate is already replying | Internal | MVP |
| Business hours and auto-reply | Away messages outside hours, per channel | Internal + WhatsApp/Messenger auto-replies | MVP |
| Response-time SLAs | Timers, overdue alerts, escalation | Internal | V2 |
| Window countdown | Show time left in 24 h window; switch to WhatsApp template when it expires | Internal | MVP |
| Contact profile (lightweight CRM) | One profile per customer across channels, with history, tags, orders | Internal | V2 |
| Spam and abuse filters | Auto-hide comments with abusive words or links (Hindi and regional words too) | Internal + hide APIs | V2 |
| Keyword routing rules | Route "refund", "price" etc. to the right team | Internal | V2 |
| Mobile app inbox | Reply on the go with push notifications | Internal (Flutter app) | V2 |
| Helpdesk handoff | Send a conversation to Freshdesk or Zoho Desk as a ticket | Vendor APIs | Later |

## WhatsApp marketing and commerce suite

This is Tuorps' main differentiator. Sprout's WhatsApp support cannot start new chats, send media templates or run chatbots; Tuorps does all three, plus UPI payments. Tuorps onboards customers as a Meta Tech Provider via Embedded Signup.

| Feature | What it does | Powered by | Phase |
| --- | --- | --- | --- |
| Embedded Signup | Customer connects their own WhatsApp number in minutes | API: Embedded Signup | MVP |
| Template manager | Create, submit, track approval; warn when Meta recategorizes to marketing | API: `message_templates` + status webhooks | MVP |
| Broadcast campaigns | Send approved templates to a contact segment, scheduled | API: Cloud API / Marketing Messages API | MVP |
| Contact import and opt-in | Upload CSV, record opt-in source and date, honor opt-outs | Internal | MVP |
| Segments | Filter by tag, city, last order, campaign response | Internal | MVP |
| Cost estimator | Show INR cost before sending (₹0.8631 marketing, ₹0.115 utility/auth) | Internal + `pricing_analytics` | MVP |
| Delivery and read tracking | Sent, delivered, read, failed per message and campaign | API: status webhooks | MVP |
| Click-to-WhatsApp ad replies | Tag chats from ads; use the free 72 h window | API: webhook referral data | MVP |
| Interactive messages | Reply buttons, list menus, CTA links | API: `type: interactive` | MVP |
| No-code chatbot builder | Keyword and menu flows, handoff to human agent | Internal | V2 |
| WhatsApp Flows | In-chat forms for leads, bookings, feedback | API: Flows | V2 |
| Catalog messages | Send single or multi-product cards from a Meta catalog | API: product messages | V2 |
| UPI payments in chat | Send order and payment request; customer pays with any UPI app | API: Payments API India (UPI intent) | V2 |
| Gateway payments | Cards, NetBanking, wallets, refunds via Razorpay, PayU, BillDesk or Zaakpay | API: Payments API India (gateway mode) | Later |
| Order notifications | Auto-send order, shipping and delivery updates from Shopify/WooCommerce | API: utility templates + vendor webhooks | V2 |
| Abandoned cart recovery | Remind shoppers with a template after N hours | Internal + store webhooks | V2 |
| OTP sending | Authentication templates for login codes | API: authentication templates | Later |
| Multi-number support | Several numbers per business (sales, support, branches) | API + internal | V2 |
| Quality and limit monitor | Show messaging limit (250 → 2,000 → auto), quality rating, alerts | API: `whatsapp_business_manager_messaging_limit`, account alerts | MVP |
| INR billing readiness | Flag WABAs not yet on INR billing before the Dec 31, 2026 deadline | Internal check | MVP |
| Business calling | In-chat voice calls with customers | API: Business Calling API | Later |

## Analytics, reporting and benchmarks

Reports must use the metrics platforms still support: Meta has replaced reach and impressions with "views" (`page_media_view`, Instagram `views`), so Tuorps standardises on views across networks.

| Feature | What it does | Powered by | Phase |
| --- | --- | --- | --- |
| Cross-network dashboard | Followers, views, engagement, top posts for all accounts | API: FB Page Insights, IG Insights, LinkedIn statistics, YouTube Analytics | MVP |
| Post performance | Views, reach, likes, comments, shares, saves, clicks per post | API: per-post insights | MVP |
| Reels and video analytics | Avg watch time, skip rate, retention, view duration | API: IG Reels metrics, FB video metrics, YouTube Analytics | MVP |
| Audience demographics | Age, gender, city, country of followers | API: IG `follower_demographics`, FB, YouTube | MVP |
| LinkedIn follower insights | Followers by function, seniority, industry, company size | API: follower statistics | MVP |
| Inbox and team reports | Volume, first-response time, resolution time per agent | Internal | MVP |
| WhatsApp campaign reports | Delivery, read rate, replies, clicks, cost in INR per campaign | API: status webhooks + `pricing_analytics` | MVP |
| Data warehouse sync | Daily snapshots so history outlives platform limits (FB 2 years, IG 90 days for account metrics) | Internal | MVP |
| PDF and PPT exports | Branded client-ready reports | Internal | MVP |
| Scheduled reports | Email or WhatsApp a report weekly or monthly | Internal | V2 |
| Custom report builder | Drag metrics and charts into a saved layout | Internal | V2 |
| Competitor benchmarks | Compare followers, posting rate, engagement with rivals | API: IG Business Discovery, YouTube public stats (FB/LinkedIn not available) | V2 |
| Hashtag performance | Track up to 30 hashtags per IG account per week | API: IG Hashtag Search | V2 |
| Link click and UTM report | Clicks from Tuorps short links, joined with Google Analytics 4 | Internal + GA4 API | V2 |
| Campaign ROI | Tie posts and WhatsApp campaigns to orders and revenue | Internal + store/payment webhooks | Later |
| Industry benchmarks | Anonymised averages across Tuorps customers by sector and city | Internal | Later |

## Listening

Owned listening ships at launch because it reuses inbox webhooks. Open-web listening is a paid add-on later, because the data costs money: X charges $0.005 per post read with 7-day search only, and commercial Reddit access needs a contract.

| Feature | What it does | Powered by | Phase |
| --- | --- | --- | --- |
| Owned mentions | Every tag and mention of the brand on its own channels | API: FB/IG/LinkedIn mention webhooks | MVP |
| Hashtag tracking | Public IG posts using tracked hashtags (30 per week) | API: IG Hashtag Search | V2 |
| YouTube keyword watch | New videos mentioning the brand (100 searches/day per project) | API: `search.list` | V2 |
| News and blog alerts | Brand mentions in Indian news sites and blogs | Internal RSS + Google News feeds | V2 |
| Reddit monitoring | Mentions in subreddits such as r/india and r/bangalore | Vendor or Reddit commercial API | Later |
| X monitoring | Keyword search across X | API: X pay-per-use (cost passed to customer) | Later |
| Sentiment (multilingual) | Positive, negative, neutral, including Hinglish and regional scripts | Vendor LLM | V2 |
| Spike alerts | Alert on WhatsApp/email when mention volume or negative sentiment jumps | Internal | V2 |
| Topic and theme clustering | Group mentions into themes (delivery, pricing, quality) | Vendor LLM | Later |
| Listening API resale | Full social listening through a partner API | Vendor (for example Brand24 API) | Later |

## Teams, approvals and agencies

Agencies are a large share of buyers in India, so Tuorps prices by brand rather than per seat and gives clients their own lightweight access.

| Feature | What it does | Powered by | Phase |
| --- | --- | --- | --- |
| Workspaces and brands | One workspace, many brands, each with its own accounts | Internal | MVP |
| Roles and permissions | Owner, admin, publisher, agent, viewer; per brand | Internal | MVP |
| Approval workflow | Draft → review → approved → scheduled, with comments | Internal | MVP |
| Client approval links | Client approves posts via a link or WhatsApp, no login needed | Internal + WhatsApp template | MVP |
| Activity log | Who published, edited, replied or deleted what | Internal | MVP |
| Multi-step approvals | Legal or brand review before publish for larger clients | Internal | V2 |
| Task board | Content requests and briefs with owners and due dates | Internal | V2 |
| White-label | Agency logo, colours and custom domain for client portal and reports | Internal | V2 |
| Client billing | Agency bills its clients through Tuorps with GST invoices | Internal + Razorpay | Later |
| Freelancer access | Time-limited access for outside designers or writers | Internal | V2 |

## AI assistant

AI features run on an LLM vendor (or a self-hosted model for on-prem customers) and always leave the final send to a human by default.

| Feature | What it does | Powered by | Phase |
| --- | --- | --- | --- |
| Caption writer | Draft captions per network from a brief, in brand tone | Vendor LLM | MVP |
| Translate and transliterate | English ↔ Hindi, Malayalam, Tamil, Kannada, Telugu, Marathi, Bengali; Hinglish | Vendor LLM | MVP |
| Suggested replies | Draft inbox replies using past answers and saved replies | Vendor LLM | V2 |
| Hashtag suggestions | Relevant, non-banned hashtags per post | Vendor LLM + internal list | V2 |
| Conversation summary | One-line summary and intent (lead, complaint, order) per thread | Vendor LLM | V2 |
| Ask your data | "Which Reel did best in Kochi last month?" answered from Tuorps analytics | Vendor LLM + internal | Later |
| WhatsApp AI agent | Answers FAQs and order status, hands off to humans | Vendor LLM + chatbot builder (check Meta's AI-provider policy) | Later |
| Brand voice profile | Learn tone from past posts; apply to all drafts | Vendor LLM | V2 |

## India-specific features (not in Sprout)

These features are why an Indian business would choose Tuorps over Sprout. Most are internal builds, so they are cheap to ship relative to their sales value.

| Feature | What it does | Powered by | Phase |
| --- | --- | --- | --- |
| Indian-language interface | App in English, Hindi and Malayalam first; more later | Internal | V2 |
| Indic composer | Write in native scripts or type phonetically ("Hinglish") and convert | Internal + vendor LLM | MVP |
| Festival and events calendar | Pre-loaded Diwali, Onam, Pongal, Eid, Christmas, Republic Day, IPL, sale days; post ideas and reminders per region | Internal | MVP |
| Regional targeting | Tag brands by state and language; suggest festivals and languages to match | Internal | V2 |
| Multi-location brands | Manage many branches, each with its own Google profile, WhatsApp number and reports | Internal + Google Business Profile API | V2 |
| Google Business Profile posts | Publish offers and updates, including recurring posts | API: GBP LocalPosts | V2 |
| INR pricing per brand | Plans priced in rupees per brand, not per seat | Internal | MVP |
| GST invoices | Auto GST invoices with GSTIN, place of supply, SAC code; downloadable | Internal + Razorpay | MVP |
| UPI and UPI AutoPay billing | Pay subscriptions by UPI, cards, NetBanking; auto-renew with reminders | Vendor: Razorpay/Cashfree | MVP |
| Fair renewals | Reminder 15 and 3 days before renewal; one-click cancel (a top Sprout complaint) | Internal | MVP |
| Shopify and WooCommerce | Product posts, catalog sync, order-update and cart-recovery WhatsApp flows | Vendor APIs | V2 |
| Shipping updates | Send shipped and delivered WhatsApp updates from Shiprocket or Delhivery | Vendor webhooks | Later |
| Lead capture to CRM | Push WhatsApp, Messenger and lead-ad leads to Zoho CRM, HubSpot or Google Sheets | API: `leadgen` webhook + vendor APIs | V2 |
| Marketplace leads | Import enquiries from IndiaMART and JustDial into the inbox | Vendor APIs (availability to confirm) | Later |
| Influencer disclosure helper | Apply "Paid partnership" labels and remind about ASCI disclosure rules | API: IG branded-content parameters + internal | V2 |
| DPDP consent records | Store opt-in purpose, source and date per contact; process withdrawals and deletion requests | Internal | MVP |
| India data residency | Host customer data in an Indian cloud region | Internal infrastructure | MVP |
| On-prem / private AI option | Run Tuorps and its AI on the customer's own servers for sensitive sectors | Internal + self-hosted model | Later |
| Mobile-first, low-data mode | Fast Flutter app; compressed media on slow networks | Internal | V2 |

The DPDP Rules were notified November 13, 2025; consent obligations and 72-hour breach reporting become enforceable on May 13, 2027 ([Mondaq summary](https://www.mondaq.com/india/privacy-protection/1708830/dpdp-act-compliance-mandate)). Building consent records now avoids a rework later.

## Platform, admin, security and integrations

| Feature | What it does | Powered by | Phase |
| --- | --- | --- | --- |
| Account connection center | Connect, reconnect and health-check every social account; warn before tokens expire (LinkedIn every 60 days) | Internal + platform OAuth | MVP |
| Token vault | Encrypted storage and automatic refresh of all access tokens | Internal | MVP |
| Webhook ingestion | Verify signatures, dedupe retries (Meta retries 36 h, LinkedIn 8 h) | Internal | MVP |
| API version manager | Track Meta, LinkedIn version sunsets and alert the team | Internal | MVP |
| Two-factor login and SSO | 2FA for all; Google/Microsoft SSO for larger clients | Internal | MVP (2FA), V2 (SSO) |
| Audit and export | Export a brand's data; delete on request | Internal | MVP |
| Notifications | Email, push and WhatsApp alerts for approvals, failures, mentions | Internal + WhatsApp utility templates | MVP |
| Public API and webhooks | Let customers and partners automate Tuorps | Internal | Later |
| Zapier / n8n / Make connectors | No-code automations | Vendor | Later |
| Google Analytics 4 link | Pull traffic and conversions for social campaigns | API: GA4 Data API | V2 |
| Usage-based billing meters | Track WhatsApp spend, AI credits, listening volume per brand | Internal | MVP |

## Out of scope and open questions

**Not possible with official APIs**

- LinkedIn and YouTube DMs; LinkedIn personal-profile analytics.
- Facebook and Instagram keyword search outside the customer's own accounts and IG hashtags.
- TikTok (banned in India).
- ShareChat and Moj publishing: no public publishing API found.

**Left out for now:** X and Threads publishing, paid ads management, and employee advocacy. Each can be added later if customers ask.

**Open questions for the team**

- Should WhatsApp messages be billed at Meta cost plus a markup, or bundled into plans?
- Which LLM vendor (or self-hosted model) for Indian-language quality and cost?
- Pricing tiers: per brand, per WhatsApp number, or both?

### Sources

- Companion API reference doc (Meta, LinkedIn, YouTube, WhatsApp endpoints and limits)
- [Google Business Profile API updates](https://developers.google.com/my-business/content/latest-updates)
- [DPDP Rules 2025 summary, Mondaq](https://www.mondaq.com/india/privacy-protection/1708830/dpdp-act-compliance-mandate)
- [X API pay-per-use rates](https://posteverywhere.ai/blog/x-twitter-api-pricing), [Reddit API commercial access](https://octolens.com/blog/reddit-api-pricing)
