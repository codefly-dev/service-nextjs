/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  images: {
    domains: [
      'lh3.googleusercontent.com',
      'avatars.githubusercontent.com',
      's.gravatar.com',
      'platform-lookaside.fbsbx.com',
      'ui-avatars.com',
      // Add any other domains you might need
    ],
    remotePatterns: [
      {
        protocol: 'https',
        hostname: '**',
      },
      {
        protocol: 'http',
        hostname: '**',
      },
    ],
  },
  i18n: {
    locales: ['en', 'fr', 'es'], // Add your supported locales here
    defaultLocale: 'en',
  },
  experimental: {
  },
};

export default nextConfig;
