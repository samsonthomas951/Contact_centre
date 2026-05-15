import type { Config } from "tailwindcss";

const config: Config = {
  content: ["./app/**/*.{ts,tsx}", "./components/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        brand: { DEFAULT: "#0b6df0", dark: "#063e8c" },
      },
    },
  },
  plugins: [],
};

export default config;
