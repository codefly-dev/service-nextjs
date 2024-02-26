import { getAccessToken, withApiAuthRequired } from "@auth0/nextjs-auth0";

async function handle(req, res) {
  const { accessToken } = await getAccessToken(req, res);
  res.status(200).json({ data: accessToken });
}

export default withApiAuthRequired(handle);
