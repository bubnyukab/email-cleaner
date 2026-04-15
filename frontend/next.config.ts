import type { NextConfig } from 'next';

const nextConfig: NextConfig = {
  experimental: {
    ppr: true,
    cacheComponents: true,
    serverSourceMaps: true,
  },
  async redirects() {
    return [
      {
        source: '/',
        destination: '/inbox',
        permanent: false,
      },
    ];
  },
};

export default nextConfig;
